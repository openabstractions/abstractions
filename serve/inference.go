package main

import (
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
  server list          registered model servers, their state and each
                       credential's spend today
  server add <name> --base URL [--api KIND --credential NAME] [--modalities LIST] [budget]
                       add a model server at the listed configuration
                       revision; also writes the complete rule on
                       host:<name> for the command line and the Panel, and
                       for a hosted server the runtime's own apply rule on
                       its credential, if the credential is already
                       registered; server list writes that same rule when
                       the credential is registered after this command
  server remove <name> remove a model server
  key issue --for PROGRAM [--credential NAME]
                       mint the gateway key for one program and print it
                       once; set it as that program's API key
  key revoke --for PROGRAM
                       destroy the program's key; the gateway refuses it next
  key list             every gateway key: program, credential, state; never
                       a key
  audit                retained inference decisions from both routes, with the
                       proof level each caller was bound at
  gateway status       the inference gateway setting, and whether the gateway
                       listens now
  gateway on [--address 127.0.0.1:PORT]
                       open the gateway on the running runtime and keep it
                       open across restarts (default address: the recorded
                       one, or 127.0.0.1:8793)
  gateway off          close the gateway and every gateway connection, and
                       keep it closed across restarts

server add:
  --base URL           a local server's address (http://127.0.0.1:11434), or a
                       hosted API root (https://openrouter.ai/api/v1); plain
                       http only on loopback
  --api KIND           makes the server hosted: openai-compatible,
                       anthropic-messages, openai-realtime,
                       deepgram-prerecorded, elevenlabs-stream,
                       stability-v2beta, fal-queue, replicate-predictions or
                       <owner>/<name>@<n>; without it the name is a local
                       kind: ollama, lmstudio or lemonade
  --credential NAME    the registered credential the service applies to it;
                       the runtime surveys the server under its own program,
                       and that program also needs an apply rule on the
                       credential (server add writes it when the credential
                       is already registered, server list when the
                       credential is registered after this command)
  --modalities LIST    comma-separated modalities the server serves: chat,
                       embed, transcription, speech, image, live or
                       <owner>/<name>@<n> (default: all six for
                       openai-compatible and the local kinds, and the API's
                       own table for every other API: live for
                       openai-realtime, transcription for
                       deepgram-prerecorded, speech for elevenlabs-stream,
                       image for stability-v2beta, fal-queue and
                       replicate-predictions, chat for anthropic-messages);
                       the router picks only a server serving the requested
                       modality
  --tokens-per-day N   the credential's daily token budget
  --micros-per-day N   the credential's daily spend budget in currency
                       millionths

Old names, accepted until the next release: host for server, --wire for
--api, --profiles for --modalities.

key issue:
  --for PROGRAM        the absolute executable path the gateway requires of
                       the peer presenting the key (python.exe is every
                       script it runs)
  --credential NAME    let the gateway's requests for this program spend
                       under a hosted credential; without it they stay
                       local-only

audit:
  --cursor N           start at sequence N (default: the oldest retained)
  --limit N            print at most N entries (default: all)

The gateway is closed by default and listens on 127.0.0.1 only. gateway on
opens it on an installed runtime; openabstractions serve runtime --gateway
127.0.0.1:<port> opens it for one foreground runtime without changing the
setting. A program with a key still needs abstraction.inference/complete on
the server, and abstraction.credentials/apply on a hosted credential:
  openabstractions rights grant --for inference --program PROGRAM --host NAME

A hosted server's survey runs under the runtime's own program. When a missing
apply rule refuses that survey, server list shows the server down with reason
"credential:<outcome> for <program>", naming the program the rule is
missing for.

Every command accepts --endpoint, --timeout and --json. The endpoint is
--endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the
installed runtime. --timeout D is a deadline for the whole command.

Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict, invalid, no_secure_store), 4 unavailable,
6 unknown.
`

func inferenceCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, inferenceUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "inference: a command is required", "openabstractions inference --help")
	}
	group := args[0]
	if group == "host" {
		// server's old name (RENAME-PLAN §3, Panel and command line step 12),
		// accepted for one release; messages name the new one.
		group = "server"
	}
	command := "inference " + group
	rest := args[1:]
	switch group {
	case "server", "key", "gateway":
		if len(rest) > 0 && isHelp(rest[0]) {
			_, err := io.WriteString(output, inferenceUsage)
			return err
		}
		if len(rest) == 0 {
			return commandMistake(diagnostics, command+": a command is required", "openabstractions inference --help")
		}
		command += " " + rest[0]
		switch command {
		case "inference server list", "inference server add", "inference server remove", "inference key issue", "inference key revoke", "inference key list",
			"inference gateway status", "inference gateway on", "inference gateway off":
		default:
			return commandMistake(diagnostics, fmt.Sprintf("%s: no such command", command), "openabstractions inference --help")
		}
		rest = rest[1:]
	case "audit":
	default:
		return commandMistake(diagnostics, fmt.Sprintf("inference: no command called %q", args[0]), "openabstractions inference --help")
	}
	if containsHelp(rest) {
		_, err := io.WriteString(output, inferenceUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	base := flags.String("base", "", "model server address")
	// --wire and --profiles are --api's and --modalities' old names,
	// accepted for one release (RENAME-PLAN §3, step 12; D62, D82).
	apiKind := flags.String("api", "", "the hosted server's API")
	flags.StringVar(apiKind, "wire", "", "old name of --api")
	credential := flags.String("credential", "", "credential name")
	tokens := flags.Int64("tokens-per-day", 0, "daily token budget")
	micros := flags.Int64("micros-per-day", 0, "daily spend budget in currency millionths")
	modalities := flags.String("modalities", "", "comma-separated modalities the server serves")
	flags.StringVar(modalities, "profiles", "", "old name of --modalities")
	program := flags.String("for", "", "program path")
	cursor := flags.Int64("cursor", 0, "audit start sequence")
	limit := flags.Int("limit", 0, "audit entries to print")
	address := flags.String("address", "", "inference gateway address, 127.0.0.1:PORT")
	positional, err := parsePositional(flags, rest)
	if err != nil {
		return badFlag(flags, diagnostics, command, inferenceUsage, rest, err)
	}
	wantsName := command == "inference server add" || command == "inference server remove"
	if wantsName != (len(positional) == 1) || len(positional) > 1 {
		if wantsName {
			return flagMistake(diagnostics, command, inferenceUsage, "name exactly one model server")
		}
		return flagMistake(diagnostics, command, inferenceUsage, "takes no arguments")
	}
	if options.budget < 0 || *tokens < 0 || *micros < 0 || *cursor < 0 || *limit < 0 {
		return flagMistake(diagnostics, command, inferenceUsage, "--timeout, budgets, --cursor and --limit must not be negative")
	}
	if *address != "" && (command != "inference gateway on" || gatewayAddress(*address) != nil) {
		return flagMistake(diagnostics, command, inferenceUsage, "--address is for gateway on and names 127.0.0.1:PORT")
	}
	needsProgram := command == "inference key issue" || command == "inference key revoke"
	if needsProgram && !identity.ValidSubjectProgram(*program) {
		return flagMistake(diagnostics, command, inferenceUsage, "--for must name an absolute executable path or msix:<package family>")
	}
	w := newWaiting(options.budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	call, done := w.call()
	operator, err := machine.ResolveInferenceOperator(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	transport := func(err error) error { return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)} }
	switch command {
	case "inference server list":
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
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "NAME\tCLASS\tKIND\tBASE\tCREDENTIAL\tMODALITIES\tREGISTERED BY\tUP\tSPEND TODAY\tBUDGET")
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
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.Entry.Name, class, h.Entry.Kind, h.Entry.Base, h.Entry.Credential,
				strings.Join(h.Entry.Profiles, ","), h.Entry.DeclaredBy, up, spend, ceiling)
		}
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "revision: %s\n", list.Revision)
		return table.Flush()
	case "inference server add", "inference server remove":
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
		if command == "inference server add" {
			entry := iclient.HostEntry{Name: name, Hosted: *apiKind != "", Kind: *apiKind, Base: *base, Credential: *credential, Profiles: splitList(*modalities)}
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
		text := fmt.Sprintf("%s %s; configuration revision %s", strings.TrimPrefix(command, "inference server "), name, change.Revision)
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
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "PROGRAM\tSTATE\tCREDENTIAL\tISSUED\tISSUED BY")
		for _, k := range list.Keys {
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
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
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "SEQ\tTIME\tROUTE\tRUNG\tPROGRAM\tHOST\tMODEL\tCREDENTIAL\tOUTCOME\tREASON\tTOKENS IN/OUT")
		for _, e := range entries {
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\n", e.Sequence, time.UnixMilli(e.UnixMs).UTC().Format(time.RFC3339), e.Route, e.Rung,
				e.Program, e.Host, e.Model, e.Credential, e.Outcome, e.Reason, e.TokensIn, e.TokensOut)
		}
		return table.Flush()
	}
	return nil
}
