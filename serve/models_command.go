package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/openabstractions/abstraction-facade/go/client"
	lend "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

const modelsUsage = `Usage: openabstractions models <command>

Lends a model file one store on this machine already holds to an engine
installed here, so that engine serves it. A lend is a link: a symbolic one, a
hard one where the operating system or the volume refuses it, and a copy only
when --copy asks for one. The original is never moved, renamed or deleted.

The object is named the way the storage inventory names it — the store and the
id that inventory gives it — and never by path. Read the names with
"openabstractions probe" or the runtime's own inventory; the path belongs to
the provider that holds the store.

A hosted model is the other way round: the model host loads the file itself,
with a llama.cpp this machine already has, and serves it through the runtime
like any other provider. One copy is loaded and every application shares it.
Its residency is held under a lease of abstraction.resource/leases@1, so the
card can be asked back: a yield request stops the engine and releases the
lease, and so does the idle time passing with nothing asking.

Commands:
  models lend <store>/<id> --to ENGINE [--copy]
                       place that object in the engine's own directory
  models unlend <entry> take one lend back: the link and the directories the
                       lend created go, the source file stays
  models lends         the ledger: every lend in force, oldest first
  models host <store>/<id> --engine PATH [--as NAME] [--embeddings]
                       declare that object as hosted: the model host serves it
                       as a model of its own, loaded once, under a lease
  models unhost <store>/<id>
                       stop hosting it; the last one withdraws the model host
                       and stops its engine
  models hosts         every hosted model, and the engine they are loaded with

models host:
  --engine PATH        absolute path of the llama-server this machine already
                       has. Nothing is bundled and nothing is downloaded: a
                       call with no engine configured is refused no_engine
  --as NAME            the name callers ask for; the store's own name for the
                       content by default, so the router's family and the
                       inventory's held_in name the same model
  --embeddings         start the engine with embeddings and publish
                       abstraction.inference/embed@1 beside chat@1
  --idle DURATION      unload a model nothing has asked for
  --engine-arg ARG     one extra argument for the engine; repeatable
  --program PATH       the model host provider; beside this command by default
  --provider-endpoint NAME, --resource-endpoint PATH
                       the provider's own OA endpoint, and where
                       abstraction.resource/leases@1 answers

A hosted model refuses in its own words: no_engine, not_permitted when no rule
lets the provider hold the card, insufficient and holders_refused when the card
could not be had, and unavailable for everything else.

models lend:
  --to ENGINE    the engine to lend to: lmstudio, lemonade or comfyui
  --copy         make a copy where no link can be placed; it is counted and
                 listed as a copy, never as a link

Lending writes into another application's directory, so it is a right the
person permits once: abstraction.storage/lend on resource engine:<name>. The
first lend is refused not_permitted and puts the question where the person
answers it; the lend after the answer goes through. Nothing here reaches the
provider directly — every call travels through the runtime, which decides the
rule and forwards.

The engine's own list can keep naming a model after unlend, until that engine's
idle-unload timer fires. The ledger, not the engine's list, says whether a lend
is in force.

Every command accepts --endpoint, --timeout and --json. The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the installed runtime.

Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (not_permitted, unsupported_engine, link_refused, already_lent,
unknown_object, invalid), 4 unavailable, 6 unknown.
`

func modelsCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, modelsUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "models: a command is required", "openabstractions models --help")
	}
	command := "models " + args[0]
	switch args[0] {
	case "host", "unhost", "hosts":
		return modelsHostCommand(command, args[1:], output, diagnostics)
	case "lend", "unlend", "lends":
	default:
		return commandMistake(diagnostics, fmt.Sprintf("models: no command called %q", args[0]), "openabstractions models --help")
	}
	if containsHelp(args[1:]) {
		_, err := io.WriteString(output, modelsUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	engine := flags.String("to", "", "engine to lend to")
	copy := flags.Bool("copy", false, "copy where no link can be placed")
	positional, err := parsePositional(flags, args[1:])
	if err != nil {
		return badFlag(flags, diagnostics, command, modelsUsage, args[1:], err)
	}
	wantsName := command != "models lends"
	if wantsName != (len(positional) == 1) || len(positional) > 1 {
		if wantsName {
			switch command {
			case "models lend":
				return flagMistake(diagnostics, command, modelsUsage, "name exactly one object, as <store>/<id>")
			default:
				return flagMistake(diagnostics, command, modelsUsage, "name exactly one ledger entry")
			}
		}
		return flagMistake(diagnostics, command, modelsUsage, "takes no arguments")
	}
	store, object := "", ""
	if command == "models lend" {
		if *engine == "" {
			return flagMistake(diagnostics, command, modelsUsage, "--to names the engine")
		}
		var ok bool
		store, object, ok = strings.Cut(positional[0], "/")
		if !ok || store == "" || object == "" {
			return flagMistake(diagnostics, command, modelsUsage, "name the object as <store>/<id>, the way the inventory names it")
		}
	} else if *engine != "" {
		return flagMistake(diagnostics, command, modelsUsage, "--to belongs to models lend")
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, modelsUsage, "--timeout must not be negative")
	}

	w := newWaiting(options.budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	call, done := w.call()
	transport, err := machine.ResolveLending(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	call, done = w.call()
	lending := lend.NewLendingClient(transport.WithContext(call))
	defer done()
	failed := func(err error) error { return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)} }

	switch command {
	case "models lend":
		result, err := lending.Lend(store, object, *engine, *copy)
		if err != nil {
			return failed(err)
		}
		if result.Outcome != lend.LendOutcomeLent {
			if options.asJSON {
				if err := writeJSON(output, result); err != nil {
					return err
				}
			}
			return refusal(command, result.Outcome.String(), result.Detail)
		}
		return credentialReport(output, options.asJSON, result,
			fmt.Sprintf("lent %s/%s to %s as %s (%s link %s); entry %s",
				result.Lend.Store, result.Lend.Object, result.Lend.Engine, result.Lend.Name,
				result.Lend.Kind, result.Lend.Link, result.Lend.ID))
	case "models unlend":
		result, err := lending.Unlend(positional[0])
		if err != nil {
			return failed(err)
		}
		if result.Outcome != lend.UnlendOutcomeUnlent {
			if options.asJSON {
				if err := writeJSON(output, result); err != nil {
					return err
				}
			}
			return refusal(command, result.Outcome.String(), result.Detail)
		}
		return credentialReport(output, options.asJSON, result,
			fmt.Sprintf("unlent %s; the engine may keep naming it until its own idle-unload timer fires", positional[0]))
	}

	var all []lend.Lend
	continuation := ""
	for pages := 0; pages < 64; pages++ {
		page, err := lending.Lends(continuation, lendingPageLimit)
		if err != nil {
			return failed(err)
		}
		if page.Outcome != lend.LendsOutcomePage {
			return refusal(command, page.Outcome.String(), "")
		}
		all = append(all, page.Lends...)
		if page.Complete {
			break
		}
		continuation = page.Continuation
	}
	if options.asJSON {
		return writeJSON(output, all)
	}
	return printLends(output, all)
}

// printLends writes the ledger as a table.
func printLends(output io.Writer, lends []lend.Lend) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintln(table, "ENTRY\tENGINE\tSTORE\tOBJECT\tNAME\tKIND\tSIZE\tCREATED\tLINK")
	for _, l := range lends {
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", l.ID, l.Engine, l.Store, l.Object, l.Name, l.Kind, l.Size, l.Created, l.Link)
	}
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintf(table, "lends: %d\n", len(lends))
	return table.Flush()
}
