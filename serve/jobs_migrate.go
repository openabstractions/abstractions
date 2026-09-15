package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
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

const jobsUsage = `Usage: openabstractions jobs <command>

Commands:
  migrate-legacy   convert legacy job records in the managed runtime root into
                   service-owned records using an operator ownership mapping

Run "openabstractions jobs migrate-legacy" for its commands.
`

const migrateUsage = `Usage: openabstractions jobs migrate-legacy <inspect|apply|abandon> [options]

Converts legacy job records found in the managed runtime job root
(<state-dir>/jobs, the same root "openabstractions serve runtime" opens) into
service-owned records. The mapping file is the only ownership authority:
nothing is inferred from file names, and active work is never assigned.

  inspect [--state-dir DIR] [--template]
      List records with state, SHA-256 digest and any refusal, followed by a
      mapping template. --template prints only the template JSON.
  apply --mapping FILE [--state-dir DIR]
      Convert every record. All records must be terminal, mapped and unchanged
      since inspection, or nothing is written. Repeating the same mapping is
      idempotent. Refuses while a runtime job host is running.
  abandon [--state-dir DIR]
      Withdraw an interrupted apply that did not finish. Refuses completed
      migrations and a running runtime job host.

Drain active legacy work through the legacy provider (jobd) before apply.

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
	if len(args) == 0 || isHelp(args[0]) {
		_, err := io.WriteString(output, jobsUsage)
		return err
	}
	if args[0] != "migrate-legacy" {
		return &exitError{exitUsage, fmt.Errorf("jobs: unknown command %q; use jobs --help", args[0])}
	}
	return migrateLegacyCommand(args[1:], output, diagnostics)
}

func migrateLegacyCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || isHelp(args[0]) {
		_, err := io.WriteString(output, migrateUsage)
		return err
	}
	sub := args[0]
	flags := flag.NewFlagSet("jobs migrate-legacy "+sub, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		// Defaults follow the usage text only when the usage text was written.
		if _, err := fmt.Fprint(diagnostics, migrateUsage); err == nil {
			flags.PrintDefaults()
		}
	}
	state := flags.String("state-dir", "", "absolute managed runtime state directory (default: current user's runtime-v1)")
	var template *bool
	var mapping *string
	switch sub {
	case "inspect":
		template = flags.Bool("template", false, "print only the mapping template JSON")
	case "apply":
		mapping = flags.String("mapping", "", "operator mapping JSON file")
	case "abandon":
	default:
		return &exitError{exitUsage, fmt.Errorf("jobs migrate-legacy: unknown command %q", sub)}
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{exitUsage, err}
	}
	if flags.NArg() != 0 {
		return &exitError{exitUsage, errors.New("jobs migrate-legacy: unexpected arguments")}
	}
	supplied := false
	flags.Visit(func(f *flag.Flag) { supplied = supplied || f.Name == "state-dir" })
	if supplied && *state == "" {
		return &exitError{exitUsage, errors.New("jobs migrate-legacy: --state-dir must be absolute")}
	}
	root, err := managedJobRoot(*state)
	if err != nil {
		return &exitError{exitUsage, fmt.Errorf("jobs migrate-legacy: %w", err)}
	}
	switch sub {
	case "inspect":
		return inspectLegacy(root, *template, output, diagnostics)
	case "apply":
		if *mapping == "" {
			return &exitError{exitUsage, errors.New("jobs migrate-legacy apply: --mapping is required")}
		}
		return applyLegacy(root, *mapping, output, diagnostics)
	default:
		return abandonLegacy(root, output)
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

// describeJobdStore reports jobd's separate legacy store, whose records this
// migration cannot see, and returns a failed write. It reads only the directory
// listing.
func describeJobdStore(managedRoot string, w io.Writer) error {
	store, origin, err := downloadserve.StoreRootOrigin()
	if err != nil {
		_, werr := fmt.Fprintf(w, "jobd legacy store: location unavailable (%v).\n", err)
		return werr
	}
	if sameDirectory(store, managedRoot) {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(store, "jobs"))
	records := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			records++
		}
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		_, werr := fmt.Fprintf(w, "jobd legacy store: %s (from %s) has no jobs directory.\n", store, origin)
		return werr
	case err != nil:
		_, werr := fmt.Fprintf(w, "jobd legacy store: %s (from %s) is unreadable: %v.\n", store, origin, err)
		return werr
	}
	_, err = fmt.Fprintf(w, `jobd legacy store: %s (from %s) holds %d job record(s).
Migration cannot see these records: it converts only records inside %s.
Instead: keep observing and finishing them through jobd, dl or jobctl, the
explicit legacy provider; submit new work through the runtime job service.
Importing records from the jobd store into the runtime root is not available.
`, store, origin, records, managedRoot)
	return err
}

func sameDirectory(a, b string) bool {
	if left, err := os.Stat(a); err == nil {
		if right, err := os.Stat(b); err == nil {
			return os.SameFile(left, right)
		}
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func inspectLegacy(root string, templateOnly bool, output, diagnostics io.Writer) error {
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
	encoded, err := json.MarshalIndent(template, "", "  ")
	if err != nil {
		return err
	}
	if templateOnly {
		if err := describeJobdStore(root, diagnostics); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "%s\n", encoded)
		return err
	}
	if _, err := fmt.Fprintf(output, "Legacy job root: %s\n", root); err != nil {
		return err
	}
	if err := describeJobdStore(root, output); err != nil {
		return err
	}
	if profile, owned, err := acceptanceprovider.RecordedExecutionProfile(root); err != nil {
		return fmt.Errorf("jobs migrate-legacy inspect %q: %w", root, err)
	} else if owned {
		if _, err := fmt.Fprintf(output, "Service owner already configured (execution profile %q).\n", profile); err != nil {
			return err
		}
	}
	if len(jobs) == 0 {
		if _, err := fmt.Fprintln(output, "No job records."); err != nil {
			return err
		}
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
	_, err = fmt.Fprintf(output, "\nMapping template (fill caller and request_key for every record):\n%s\n", encoded)
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

func migrationExit(action string, err error) error {
	switch {
	case errors.Is(err, acceptanceprovider.ErrHostActive):
		return &exitError{exitHostActive, fmt.Errorf("%s: a runtime job host is running on this root; stop it first: %w", action, err)}
	case errors.Is(err, acceptanceprovider.ErrLegacyMigrationRefused):
		return &exitError{exitRefused, fmt.Errorf("%s: %w", action, err)}
	case errors.Is(err, acceptanceprovider.ErrLegacyMigrationConflict):
		return &exitError{exitConflict, fmt.Errorf("%s: %w", action, err)}
	case errors.Is(err, acceptanceprovider.ErrLegacyMappingInvalid):
		return &exitError{exitUsage, fmt.Errorf("%s: %w", action, err)}
	}
	return fmt.Errorf("%s: %w", action, err)
}

func applyLegacy(root, path string, output, diagnostics io.Writer) error {
	const action = "jobs migrate-legacy apply"
	f, err := os.Open(path)
	if err != nil {
		return &exitError{exitUsage, fmt.Errorf("%s: %w", action, err)}
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
		return &exitError{exitUsage, fmt.Errorf("%s: invalid mapping file: %w", action, err)}
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
		return migrationExit(action, errors.Join(err, refusals))
	}
	status := "complete"
	if result.AlreadyMigrated {
		status = "already complete"
	}
	if _, err := fmt.Fprintf(output, "Legacy migration %s: %d records\nlogical owner: %s\nhistory epoch: %s\n", status, len(result.Converted), result.LogicalOwner, result.HistoryEpoch); err != nil {
		return errors.Join(err, refusals)
	}
	for _, c := range result.Converted {
		if _, err := fmt.Fprintf(output, "converted %s request_key=%s\n", c.Receipt.OperationId, strings.TrimSpace(keys[c.Receipt.OperationId])); err != nil {
			return errors.Join(err, refusals)
		}
	}
	return refusals
}

func abandonLegacy(root string, output io.Writer) error {
	const action = "jobs migrate-legacy abandon"
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		_, err = fmt.Fprintf(output, "No managed job root at %s.\n", root)
		return err
	}
	if err := acceptanceprovider.AbandonLegacyMigration(root); err != nil {
		return migrationExit(action, err)
	}
	_, err := fmt.Fprintf(output, "No unfinished legacy migration remains in %s.\n", root)
	return err
}
