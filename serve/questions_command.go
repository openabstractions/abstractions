package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
)

const questionsUsage = `Usage: openabstractions questions <command> [options]

Lists questions admitted to this account's runtime and records one answer
through its authorized operator service. The answer is a person's choice;
the application decides whether that choice permits its proposed action.

Commands:
  pending                 list current pending questions, their options and
                          the program that asked each
  answer <id> <option>    record one option shown for that question
  permissions             list pending OA permission requests; the person
                          answers each in the OA permission dialog or Panel

Every command accepts --endpoint, --timeout and --json. The endpoint is
--endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the
installed runtime. pending and answer use abstraction.asks/operator@1;
permissions uses abstraction.asks/permission-operator@1 and only reads.
Each operator policy checks this program's native identity. It does not use
the old standalone asks.json or admin.secret.

An answer whose reply was lost may already be recorded. Inspect the Panel's
Questions page, or explicitly repeat the same id and option; that repeat
returns the original decision. Never infer approval from a transport error.

With --endpoint, --runtime-program PATH verifies that absolute executable under this account.

Exit codes: 0 listed or answered, 1 resolution failure, 2 usage,
3 typed refusal, 6 uncertain answer or changing list.
`

// questionOperator is the operator profile resolved for this command. The
// service owns authorization and checks the caller's native program proof.
type questionOperator interface {
	ListQuestionsContext(context.Context, string, int64) (askswire.OperatorPage, error)
	AnswerQuestionContext(context.Context, string, string) (askswire.OperatorDecision, error)
}

func questionsCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, questionsUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "questions: a command is required", "openabstractions questions --help")
	}
	verb := args[0]
	if verb != "pending" && verb != "answer" && verb != "permissions" {
		return commandMistake(diagnostics, fmt.Sprintf("questions: no command called %q", verb), "openabstractions questions --help")
	}
	if containsHelp(args[1:]) {
		_, err := io.WriteString(output, questionsUsage)
		return err
	}
	command := "questions " + verb
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	var options serviceOptions
	options.bind(flags)
	positional, err := parsePositional(flags, args[1:])
	if err != nil {
		return badFlag(flags, diagnostics, command, questionsUsage, args[1:], err)
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, questionsUsage, "--timeout must not be negative")
	}
	if (verb == "pending" || verb == "permissions") && len(positional) != 0 {
		return flagMistake(diagnostics, command, questionsUsage, verb+" takes no arguments")
	}
	if verb == "answer" && (len(positional) != 2 || !questionArgument(positional[0]) || !questionArgument(positional[1])) {
		return flagMistake(diagnostics, command, questionsUsage, "answer needs one question id and one option shown for it")
	}
	if options.budget == 0 {
		options.budget = callBudget
	}
	w := newWaiting(options.budget)
	defer w.stop()
	machine, source, err := options.machine(diagnostics, command, questionsUsage)
	if err != nil {
		return err
	}
	call, done := w.call()
	if verb == "permissions" {
		permissions, err := machine.ResolveAsksPermissionOperator(call, client.Requirements{Scope: client.ScopeLocal})
		done()
		if err != nil {
			return notResolved(command, source, err)
		}
		records, err := listPendingPermissions(w.ctx, permissions)
		if err != nil {
			return err
		}
		return printPendingPermissions(output, records, options.asJSON)
	}
	operator, err := machine.ResolveAsksOperator(call, client.Requirements{Scope: client.ScopeLocal})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	if verb == "pending" {
		records, err := listPendingQuestions(w.ctx, operator)
		if err != nil {
			return err
		}
		return printPendingQuestions(output, records, questionAskers(w.ctx, operator, records), options.asJSON)
	}
	records, err := listQuestions(w.ctx, operator)
	if err != nil {
		return err
	}
	if err := answerableQuestion(records, positional[0], positional[1]); err != nil {
		return err
	}
	decision, err := answerQuestion(w.ctx, operator, positional[0], positional[1])
	if err != nil {
		return err
	}
	return printQuestionAnswer(output, decision, options.asJSON)
}

func answerableQuestion(records []askswire.RecordMetadata, id, option string) error {
	for _, record := range records {
		if record.ID != id {
			continue
		}
		if record.Key == asks.FirstUseKey {
			return &exitError{exitRefusedCall, errors.New("questions answer: answer this first-use rights question in the Panel so its rule is written")}
		}
		if record.Key == asks.SystemSecurityLogKey && option == "once" {
			// ASK-S1: once is answered in the native prompt, which raises
			// the administrator prompt right after it (FAC-H3).
			return &exitError{exitRefusedCall, errors.New("questions answer: answer once in the OpenAbstractions prompt; Windows asks for administrator approval right after it")}
		}
		return nil
	}
	return &exitError{exitRefusedCall, fmt.Errorf("questions answer: no question %s in the current list", id)}
}

func questionArgument(value string) bool {
	return len(value) > 0 && len(value) <= 128 && utf8.ValidString(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

// ListQuestions is latest-book enumeration. A changing book reports gap, so
// discard the partial list and let the person explicitly run pending again.
func listPendingQuestions(ctx context.Context, operator questionOperator) ([]askswire.RecordMetadata, error) {
	records, err := listQuestions(ctx, operator)
	if err != nil {
		return nil, err
	}
	return pendingRecords(records), nil
}

// permissionOperator is the read side of abstraction.asks/permission-operator@1.
// The ordinary operator profile omits typed permission requests (ASK contract),
// so this command lists them separately and never answers them.
type permissionOperator interface {
	ListPermissionsContext(context.Context, string, int64) (askswire.OperatorPage, error)
}

func listPendingPermissions(ctx context.Context, operator permissionOperator) ([]askswire.RecordMetadata, error) {
	records, err := listPages(ctx, "permissions", operator.ListPermissionsContext)
	if err != nil {
		return nil, err
	}
	return pendingRecords(records), nil
}

func pendingRecords(records []askswire.RecordMetadata) []askswire.RecordMetadata {
	pending := []askswire.RecordMetadata{}
	for _, record := range records {
		if record.Option == "" {
			pending = append(pending, record)
		}
	}
	return pending
}

func listQuestions(ctx context.Context, operator questionOperator) ([]askswire.RecordMetadata, error) {
	return listPages(ctx, "pending", operator.ListQuestionsContext)
}

func listPages(ctx context.Context, verb string, list func(context.Context, string, int64) (askswire.OperatorPage, error)) ([]askswire.RecordMetadata, error) {
	const pageSize = 64
	const maxPages = 64 // the Book holds at most 4096 retained records.
	command := "questions " + verb
	cursor := ""
	var records []askswire.RecordMetadata
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		page, err := list(ctx, cursor, pageSize)
		if err != nil {
			return nil, &exitError{exitNotResolved, fmt.Errorf("%s: list could not be read: %w", command, err)}
		}
		switch page.Outcome {
		case askswire.OperatorPageOutcomePage:
			records = append(records, page.Records...)
			if page.Complete {
				return records, nil
			}
			if page.Next == "" || page.Next == cursor {
				return nil, &exitError{exitUncertain, fmt.Errorf("%s: the list did not advance; run %s again", command, verb)}
			}
			cursor = page.Next
		case askswire.OperatorPageOutcomeGap:
			return nil, &exitError{exitUncertain, fmt.Errorf("%s: questions changed while listing; run %s again", command, verb)}
		case askswire.OperatorPageOutcomeUnavailable:
			return nil, &exitError{exitUnavailable, fmt.Errorf("%s: the question store is unavailable", command)}
		default:
			return nil, &exitError{exitRefusedCall, fmt.Errorf("%s: %s", command, page.Outcome)}
		}
	}
	return nil, &exitError{exitUncertain, fmt.Errorf("%s: too many pages; run %s again", command, verb)}
}

func answerQuestion(ctx context.Context, operator questionOperator, id, option string) (askswire.OperatorDecision, error) {
	decision, err := operator.AnswerQuestionContext(ctx, id, option)
	if err != nil {
		return askswire.OperatorDecision{}, &exitError{exitUncertain, fmt.Errorf("questions answer: reply uncertain for %s; inspect Questions or repeat this same id and option: %w", id, err)}
	}
	switch decision.Outcome {
	case askswire.OperatorDecisionOutcomeAnswered:
		if decision.Record == nil || decision.Record.ID != id || decision.Record.Option != option {
			return askswire.OperatorDecision{}, &exitError{exitUncertain, fmt.Errorf("questions answer: inconsistent decision for %s; inspect Questions", id)}
		}
		return decision, nil
	case askswire.OperatorDecisionOutcomeUnavailable:
		return askswire.OperatorDecision{}, &exitError{exitUncertain, fmt.Errorf("questions answer: outcome uncertain for %s; inspect Questions or repeat this same id and option", id)}
	default:
		return askswire.OperatorDecision{}, &exitError{exitRefusedCall, fmt.Errorf("questions answer: %s for %s", decision.Outcome, id)}
	}
}

// questionAskers reads, for each listed question, the bound program that
// asked it (asks ASK-O11), so two programs with one file name read apart. A
// question without a recorded program, a failed read and an operator
// service older than the read leave the record's display name alone.
func questionAskers(ctx context.Context, operator questionOperator, records []askswire.RecordMetadata) map[string]string {
	reader, ok := operator.(askclient.AskerReader)
	if !ok {
		return nil
	}
	var askers map[string]string
	for _, record := range records {
		read, err := reader.ReadQuestionAskerContext(ctx, record.ID)
		if err != nil || read.Program == "" {
			continue
		}
		if askers == nil {
			askers = map[string]string{}
		}
		askers[record.ID] = read.Program
	}
	return askers
}

// printPendingQuestions lists each question with its asker: the bound
// program when the service recorded one, else the record's display name.
func printPendingQuestions(output io.Writer, records []askswire.RecordMetadata, askers map[string]string, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(output).Encode(struct {
			Outcome string                    `json:"outcome"`
			Records []askswire.RecordMetadata `json:"records"`
			Askers  map[string]string         `json:"askers,omitempty"`
		}{"page", records, askers})
	}
	if len(records) == 0 {
		_, err := io.WriteString(output, "No pending questions.\n")
		return err
	}
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for _, record := range records {
		asker := record.Asker
		if program := askers[record.ID]; program != "" {
			asker = program
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", record.ID, record.Text, strings.Join(record.Options, "|"), asker); err != nil {
			return err
		}
	}
	return w.Flush()
}

func printPendingPermissions(output io.Writer, records []askswire.RecordMetadata, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(output).Encode(struct {
			Outcome string                    `json:"outcome"`
			Records []askswire.RecordMetadata `json:"records"`
		}{"page", records})
	}
	if len(records) == 0 {
		_, err := io.WriteString(output, "No pending permission requests.\n")
		return err
	}
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for _, record := range records {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", record.ID, record.About, record.Asked); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(output, "Answer each request in the OA permission dialog or the Panel.\n")
	return err
}

func printQuestionAnswer(output io.Writer, decision askswire.OperatorDecision, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(output).Encode(decision)
	}
	_, err := fmt.Fprintf(output, "%s: %s (%s)\n", decision.Record.ID, decision.Record.Option, decision.Outcome)
	return err
}
