package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	logging "github.com/openabstractions/abstraction-logging/go"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	"github.com/openabstractions/abstraction-logging/go/logview"
)

const logsUsage = `Usage: openabstractions logs follow --requests [options]
       openabstractions logs follow NAME [--contract inference] [options]
       openabstractions logs --app NAME [--follow] [options]

Read one registered application's OA records, or the runtime's records about
this command's own requests. NAME is its registered application name.
The service checks the existing application-log
or caller-log permission before every page. It reads no provider files.

  --requests        read only records about this command's own requests
  --contract NAME   narrow to a module such as inference or an exact service
  --follow          begin at the current end and follow later records
  --level LEVEL     minimum level: debug, info, warn, error, or an integer
  --tag TAG         require this exact attribute key, or key=value
  --since TIME      include records at or after this RFC 3339 time
  --until TIME      include records at or before this RFC 3339 time
  --json            emit one JSON record per line

Every page is bounded to 64 records and 65536 encoded bytes. A history gap
prints an explicit line and stops; run the command again to choose a fresh end.
Ctrl+C stops following. --timeout bounds the whole command; zero means none.
--endpoint and --runtime-program use the common native runtime connection.
`

type logsHistory interface {
	ReadContext(context.Context, logclient.ApplicationLogTarget, string, int64, int64) (logclient.ApplicationLogPage, error)
}
type logsObserver interface {
	ObserveContext(context.Context, logclient.ApplicationLogTarget, string, int64, int64, int64) (logclient.ApplicationLogPage, error)
}

func logsCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || containsHelp(args) {
		_, err := io.WriteString(output, logsUsage)
		return err
	}
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options serviceOptions
	options.bind(flags)
	var app, contract, level, tag, since, until string
	var requests bool
	var follow bool
	flags.BoolVar(&requests, "requests", false, "read records about this native caller")
	flags.StringVar(&contract, "contract", "", "module or exact service filter")
	flags.StringVar(&app, "app", "", "registered application name")
	flags.StringVar(&level, "level", "", "minimum log level")
	flags.StringVar(&tag, "tag", "", "attribute key or key=value")
	flags.StringVar(&since, "since", "", "RFC 3339 lower time bound")
	flags.StringVar(&until, "until", "", "RFC 3339 upper time bound")
	flags.BoolVar(&follow, "follow", false, "follow from the current end")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, "logs", logsUsage, args, err)
	}
	app, follow, err = logsSelection(positional, app, follow, requests)
	if err != nil || options.budget < 0 {
		return flagMistake(diagnostics, "logs", logsUsage, "choose one registered application or --requests; timeout must not be negative")
	}
	filter, err := logview.ParseContract(contract, level, tag, since, until)
	if err != nil {
		return flagMistake(diagnostics, "logs", logsUsage, err.Error())
	}
	w := newWaiting(options.budget)
	defer w.stop()
	machine, source, err := options.machine(diagnostics, "logs", logsUsage)
	if err != nil {
		return err
	}
	if requests {
		ctx, cancel := w.call()
		reader, err := machine.ResolveLogApplicationReader(ctx, client.Requirements{Scope: client.ScopeLocal})
		cancel()
		if err != nil {
			return notResolved("logs", source, err)
		}
		from := time.Time{}
		if since != "" {
			from, _ = time.Parse(time.RFC3339Nano, since)
		}
		pages := callerLogsHistory{reader: reader, since: from}
		return readLogs(w.ctx, pages, pages, logclient.ApplicationLogTarget{}, filter, follow, output, options.asJSON)
	}
	ctx, cancel := w.call()
	directory, err := machine.ResolveApplications(ctx, client.Requirements{Scope: client.ScopeLocal})
	cancel()
	if err != nil {
		return notResolved("logs", source, err)
	}
	ctx, cancel = w.call()
	program, err := client.RegisteredApplicationProgram(ctx, directory, app)
	cancel()
	if err != nil {
		return err
	}
	ctx, cancel = w.call()
	reader, err := machine.ResolveLogApplicationReader(ctx, client.Requirements{Scope: client.ScopeLocal})
	cancel()
	if err != nil {
		return notResolved("logs", source, err)
	}
	var observer logsObserver
	if follow {
		ctx, cancel := w.call()
		resolved, err := machine.ResolveLogApplicationObserver(ctx, client.Requirements{Scope: client.ScopeLocal})
		cancel()
		if err != nil {
			return notResolved("logs", source, err)
		}
		observer, err = resolved.WithTimeout(30 * time.Second)
		if err != nil {
			return err
		}
	}
	return readLogs(w.ctx, reader, observer, logclient.ApplicationLogTarget{Selection: logclient.ApplicationLogSelectionOneApp, Program: program}, filter, follow, output, options.asJSON)
}

// These adapters reuse the shared native client wait without naming an app.
type callerLogsHistory struct {
	reader interface {
		ReadAboutCallerContext(context.Context, time.Time, string, int64, int64) (logclient.ApplicationLogPage, error)
		ObserveAboutCallerContext(context.Context, time.Time, string, int64, int64, int64) (logclient.ApplicationLogPage, error)
	}
	since time.Time
}

func (p callerLogsHistory) ReadContext(ctx context.Context, _ logclient.ApplicationLogTarget, cursor string, count, size int64) (logclient.ApplicationLogPage, error) {
	return p.reader.ReadAboutCallerContext(ctx, p.since, cursor, count, size)
}
func (p callerLogsHistory) ObserveContext(ctx context.Context, _ logclient.ApplicationLogTarget, cursor string, count, size, wait int64) (logclient.ApplicationLogPage, error) {
	return p.reader.ObserveAboutCallerContext(ctx, p.since, cursor, count, size, wait)
}

func logsSelection(positional []string, app string, follow, requests bool) (string, bool, error) {
	if len(positional) > 0 && positional[0] == "follow" {
		follow = true
		positional = positional[1:]
	}
	if len(positional) == 1 && app == "" {
		app = positional[0]
		positional = nil
	}
	if len(positional) != 0 || (app == "") != requests {
		return "", false, errors.New("logs: choose app or requests")
	}
	return app, follow, nil
}

func readLogs(ctx context.Context, reader logsHistory, observer logsObserver, target logclient.ApplicationLogTarget, filter logview.RecordFilter, follow bool, output io.Writer, asJSON bool) error {
	cursor := ""
	if follow {
		cursor = "end"
	}
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var page logclient.ApplicationLogPage
		var err error
		if follow && !first {
			page, err = observer.ObserveContext(ctx, target, cursor, 64, 65536, 25000)
		} else {
			page, err = reader.ReadContext(ctx, target, cursor, 64, 65536)
		}
		first = false
		if err != nil {
			return err
		}
		if page.Outcome != wire.HistoryPageOutcomePage {
			message := "logs: " + page.Outcome.String()
			if page.Outcome == wire.HistoryPageOutcomeGap {
				message = "History gap: retained records may be missing. Start a new logs command to continue."
			}
			if page.Outcome == wire.HistoryPageOutcomeNotPermitted {
				message = "Permission refused: the native logging service did not permit this read. Review the log-read permission in the OpenAbstractions Panel."
			}
			if _, err := fmt.Fprintln(output, message); err != nil {
				return err
			}
			return &exitError{exitRefusedCall, errors.New(message)}
		}
		for _, record := range page.Records {
			if !filter.Keep(record, logging.GapMessage) {
				continue
			}
			if asJSON {
				var line bytes.Buffer
				err = json.Compact(&line, wire.EncodeRecord(&record))
				if err == nil {
					line.WriteByte('\n')
					_, err = output.Write(line.Bytes())
				}
			} else {
				_, err = fmt.Fprintf(output, "%s [%d] %s\n", record.Time, record.Level, record.Msg)
			}
			if err != nil {
				return err
			}
		}
		cursor = page.Next
		if !follow && page.AtEnd {
			return nil
		}
	}
}
