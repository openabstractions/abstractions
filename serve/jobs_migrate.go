package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

// LegacyMappingFormat identifies version 1 of the operator mapping file.
const LegacyMappingFormat = "openabstractions.job/legacy-mapping@1"

const maxMappingBytes = 16 << 20

// Exit codes of `openabstractions jobs migrate-legacy`.
const (
	exitUsage      = 2 // usage error or invalid mapping file
	exitRefused    = 3 // one or more records refused; nothing written
	exitConflict   = 4 // a different migration or service owner already exists
	exitHostActive = 5 // a runtime job host holds the managed root
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

const jobsUsage = jobsServiceUsage

const migrateUsage = `Usage: openabstractions jobs migrate-legacy <inspect|apply|abandon> [options]

Converts legacy job records found in the managed runtime job root
(<state-dir>/jobs, the same root "openabstractions serve runtime" opens) into
service-owned records. The mapping file is the only ownership authority:
nothing is inferred from file names, and active work is never assigned.

  inspect [--state-dir DIR] [--template] [--json]
      List records with state, SHA-256 digest and any refusal, and how many
      still need a mapping entry. --template prints only the mapping template
      JSON; --json prints one JSON document with the records and the template.
  apply --mapping FILE [--state-dir DIR]
      Convert every record. All records must be terminal, mapped and unchanged
      since inspection, or nothing is written. Repeating the same mapping is
      idempotent. Refuses while a runtime job host is running.
  abandon [--state-dir DIR]
      Withdraw an interrupted apply that did not finish. Refuses completed
      migrations and a running runtime job host.

Active legacy records are refused; the legacy provider that could finish them
was removed in 0.1.8.

Mapping file (JSON, unknown or duplicate fields refused):
  {
    "format": "` + LegacyMappingFormat + `",
    "assignments": [{
      "operation_id": "<record id from inspect>",
      "record_sha256": "<digest from inspect>",
      "caller": {"account_kind": "windows|posix", "principal": "<SID or UID>",
                 "program": "<absolute path of the caller executable>"},
      "request_key": "<key the caller will use with the runtime history epoch>",
      "submission": {"kind": "download", "spec": <record spec JSON>,
                     "required_guarantees": []}
    }]
  }

Exit codes: 0 success, 1 other failure, 2 usage or invalid mapping,
3 records refused (typed reasons on stderr), 4 conflicting migration or owner,
5 runtime job host running.
`

func isHelp(arg string) bool { return arg == "--help" || arg == "-h" || arg == "help" }

func jobsCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, jobsUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "jobs: a command is required", "openabstractions jobs --help")
	}
	switch args[0] {
	case "migrate-legacy":
		return migrateLegacyCommand(args[1:], output, diagnostics)
	case "list", "show", "wait", "cancel", "result":
		return jobsServiceCommand(args[0], args[1:], output, diagnostics)
	}
	return commandMistake(diagnostics, fmt.Sprintf("jobs: no command called %q", args[0]), "openabstractions jobs --help")
}

func migrateLegacyCommand(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, migrateUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "jobs migrate-legacy: a command is required", "openabstractions jobs migrate-legacy --help")
	}
	sub := args[0]
	flags := flag.NewFlagSet("jobs migrate-legacy "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	state := flags.String("state-dir", "", "absolute managed runtime state directory (default: current user's runtime-v1)")
	var template, asJSON *bool
	var mapping *string
	switch sub {
	case "inspect":
		template = flags.Bool("template", false, "print only the mapping template JSON")
		asJSON = flags.Bool("json", false, "print one JSON document with the records and the mapping template")
	case "apply":
		mapping = flags.String("mapping", "", "operator mapping JSON file")
	case "abandon":
	default:
		return commandMistake(diagnostics, fmt.Sprintf("jobs migrate-legacy: no command called %q", sub), "openabstractions jobs migrate-legacy --help")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return badFlag(flags, diagnostics, "jobs migrate-legacy "+sub, migrateUsage, args[1:], err)
	}
	command := "jobs migrate-legacy " + sub
	if flags.NArg() != 0 {
		return flagMistake(diagnostics, command, migrateUsage, "unexpected arguments")
	}
	supplied := false
	flags.Visit(func(f *flag.Flag) { supplied = supplied || f.Name == "state-dir" })
	if supplied && *state == "" {
		return flagMistake(diagnostics, command, migrateUsage, "--state-dir must be absolute")
	}
	root, err := managedJobRoot(*state)
	if err != nil {
		return flagMistake(diagnostics, command, migrateUsage, err.Error())
	}
	// apply and abandon write the store; the default state is the account's.
	refuse := func() error {
		if supplied {
			return nil
		}
		return refuseVirtualizedProfile("jobs migrate-legacy", bootstrap.CurrentProfileView)
	}
	switch sub {
	case "inspect":
		return inspectLegacy(root, *template, *asJSON, output, diagnostics)
	case "apply":
		if *mapping == "" {
			return flagMistake(diagnostics, command, migrateUsage, "--mapping is required")
		}
		if err := refuse(); err != nil {
			return err
		}
		return applyLegacy(root, *mapping, output, diagnostics)
	default:
		if err := refuse(); err != nil {
			return err
		}
		return abandonLegacy(root, output, diagnostics)
	}
}

type mappingFile struct {
	Format      string         `json:"format"`
	Assignments []mappingEntry `json:"assignments"`
}

type mappingEntry struct {
	OperationID  string            `json:"operation_id"`
	RecordSHA256 string            `json:"record_sha256"`
	Caller       mappingCaller     `json:"caller"`
	RequestKey   string            `json:"request_key"`
	Submission   mappingSubmission `json:"submission"`
}

type mappingCaller struct {
	AccountKind string `json:"account_kind"`
	Principal   string `json:"principal"`
	Program     string `json:"program"`
}

type mappingSubmission struct {
	Kind               string          `json:"kind"`
	Spec               json.RawMessage `json:"spec"`
	RequiredGuarantees []string        `json:"required_guarantees"`
}

func inspectLegacy(root string, templateOnly, asJSON bool, output, diagnostics io.Writer) error {
	jobs, err := acceptanceprovider.InspectLegacyJobs(root)
	if err != nil {
		return fmt.Errorf("jobs migrate-legacy inspect %q: %w", root, err)
	}
	accountKind := "posix"
	if runtime.GOOS == "windows" {
		accountKind = "windows"
	}
	template := mappingFile{Format: LegacyMappingFormat, Assignments: []mappingEntry{}}
	for _, j := range jobs {
		if j.Kind == "" {
			continue
		}
		template.Assignments = append(template.Assignments, mappingEntry{OperationID: j.OperationID, RecordSHA256: j.RecordSHA256,
			Caller: mappingCaller{AccountKind: accountKind}, Submission: mappingSubmission{Kind: j.Kind, Spec: j.Spec, RequiredGuarantees: []string{}}})
	}
	if templateOnly {
		encoded, err := json.MarshalIndent(template, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "%s\n", encoded)
		return err
	}
	profile, owned, err := acceptanceprovider.RecordedExecutionProfile(root)
	if err != nil {
		return fmt.Errorf("jobs migrate-legacy inspect %q: %w", root, err)
	}
	if asJSON {
		type inspectReport struct {
			Root             string                         `json:"root"`
			ServiceOwned     bool                           `json:"service_owned"`
			ExecutionProfile string                         `json:"execution_profile,omitempty"`
			Jobs             []acceptanceprovider.LegacyJob `json:"jobs"`
			MappingTemplate  mappingFile                    `json:"mapping_template"`
		}
		return writeJSON(output, inspectReport{Root: root, ServiceOwned: owned, ExecutionProfile: profile, Jobs: jobs, MappingTemplate: template})
	}
	if _, err := fmt.Fprintf(output, "Legacy job root: %s\n", root); err != nil {
		return err
	}
	if owned {
		if _, err := fmt.Fprintf(output, "Service owner already configured (execution profile %q).\n", profile); err != nil {
			return err
		}
	}
	if len(jobs) == 0 {
		_, err := fmt.Fprintln(output, "No job records.")
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "OPERATION\tSTATE\tSHA256\tREFUSAL"); err != nil {
		return err
	}
	for _, j := range jobs {
		state, refusal := string(j.State), string(j.Refusal)
		if state == "" {
			state = "-"
		}
		if refusal == "" {
			refusal = "-"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", j.OperationID, state, j.RecordSHA256, refusal); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if len(template.Assignments) == 0 {
		return nil
	}
	_, err = fmt.Fprintf(output, "%d record(s) need a mapping entry; run jobs migrate-legacy inspect --json for the fillable template.\n", len(template.Assignments))
	return err
}

// refuseDuplicateNames rejects any JSON object that repeats a member name, so a
// later duplicate cannot silently replace a reviewed value.
func refuseDuplicateNames(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				name, err := d.Token()
				if err != nil {
					return err
				}
				key, _ := name.(string)
				if seen[key] {
					return fmt.Errorf("duplicate field %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	return walk()
}

// decodeLegacyMapping is the strict decoder for LegacyMappingFormat.
func decodeLegacyMapping(data []byte) (acceptanceprovider.LegacyMapping, map[string]string, error) {
	var result acceptanceprovider.LegacyMapping
	if len(data) > maxMappingBytes {
		return result, nil, errors.New("mapping file too large")
	}
	if err := refuseDuplicateNames(data); err != nil {
		return result, nil, err
	}
	var file mappingFile
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&file); err != nil {
		return result, nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return result, nil, errors.New("trailing data after mapping")
	}
	if file.Format != LegacyMappingFormat {
		return result, nil, fmt.Errorf("format must be %q", LegacyMappingFormat)
	}
	keys := map[string]string{}
	for i, entry := range file.Assignments {
		scope, err := host.OwnerProgramScope(entry.Caller.AccountKind, entry.Caller.Principal, entry.Caller.Program)
		if err != nil {
			return result, nil, fmt.Errorf("assignment %d caller: %w", i, err)
		}
		if len(entry.Submission.Spec) == 0 || entry.Submission.Kind == "" {
			return result, nil, fmt.Errorf("assignment %d: submission kind and spec required", i)
		}
		result.Assignments = append(result.Assignments, acceptanceprovider.LegacyAssignment{OperationID: entry.OperationID, RecordSHA256: entry.RecordSHA256,
			CallerScope: scope, Key: entry.RequestKey, Kind: entry.Submission.Kind, Spec: bytes.Clone(entry.Submission.Spec), RequiredGuarantees: entry.Submission.RequiredGuarantees})
		keys[entry.OperationID] = entry.RequestKey
	}
	return result, keys, nil
}

func migrationExit(diagnostics io.Writer, action string, err error) error {
	switch {
	case errors.Is(err, acceptanceprovider.ErrHostActive):
		return &exitError{exitHostActive, fmt.Errorf("%s: a runtime job host is running on this root; stop it first: %w", action, err)}
	case errors.Is(err, acceptanceprovider.ErrLegacyMigrationRefused):
		return &exitError{exitRefused, fmt.Errorf("%s: %w", action, err)}
	case errors.Is(err, acceptanceprovider.ErrLegacyMigrationConflict):
		return &exitError{exitConflict, fmt.Errorf("%s: %w", action, err)}
	case errors.Is(err, acceptanceprovider.ErrLegacyMappingInvalid):
		return flagMistake(diagnostics, action, migrateUsage, err.Error())
	}
	return fmt.Errorf("%s: %w", action, err)
}

func applyLegacy(root, path string, output, diagnostics io.Writer) error {
	const action = "jobs migrate-legacy apply"
	f, err := os.Open(path)
	if err != nil {
		// --mapping names a well-formed path; opening it is a run-time
		// failure (exitNotResolved), not a usage mistake.
		return &exitError{exitNotResolved, fmt.Errorf("%s: %s", action, pathProblem("--mapping", path, err))}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxMappingBytes+1))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	mapping, keys, err := decodeLegacyMapping(data)
	if err != nil {
		return flagMistake(diagnostics, action, migrateUsage, fmt.Sprintf("invalid mapping file: %v", err))
	}
	result, err := acceptanceprovider.MigrateLegacy(root, downloadserve.LegacySinkExecution{}, mapping)
	// The migration result stands whether or not a refusal line was written; a
	// failed write joins the command's result.
	var refusals error
	for _, refusal := range result.Refused {
		if _, werr := fmt.Fprintf(diagnostics, "refused %s: %s\n", refusal.Entry, refusal.Reason); werr != nil {
			refusals = errors.Join(refusals, werr)
		}
	}
	if err != nil {
		return migrationExit(diagnostics, action, errors.Join(err, refusals))
	}
	status := "complete"
	if result.AlreadyMigrated {
		status = "already complete"
	}
	if _, err := fmt.Fprintf(output, "Legacy migration %s: %d records\nlogical owner: %s\nhistory epoch: %s\n", status, len(result.Converted), result.LogicalOwner, result.HistoryEpoch); err != nil {
		return errors.Join(err, refusals)
	}
	for _, c := range result.Converted {
		if _, err := fmt.Fprintf(output, "converted %s request_key=%s\n", c.Receipt.OperationID, strings.TrimSpace(keys[c.Receipt.OperationID])); err != nil {
			return errors.Join(err, refusals)
		}
	}
	return refusals
}

func abandonLegacy(root string, output, diagnostics io.Writer) error {
	const action = "jobs migrate-legacy abandon"
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		_, err = fmt.Fprintf(output, "No managed job root at %s.\n", root)
		return err
	}
	if err := acceptanceprovider.AbandonLegacyMigration(root); err != nil {
		return migrationExit(diagnostics, action, err)
	}
	_, err := fmt.Fprintf(output, "No unfinished legacy migration remains in %s.\n", root)
	return err
}
