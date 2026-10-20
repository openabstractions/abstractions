package main

import (
	"context"
	"errors"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
)

func applicationV3AuditAnswer(record *appmodel.CallRecord, result applications.CallResult) {
	record.Outcome = result.Outcome.String()
	var value any
	if answer := result.Answer; answer != nil {
		switch {
		case answer.Description != nil:
			value = answer.Description
			record.Outcome = answer.Description.Outcome.String()
		case answer.Contexts != nil:
			value = answer.Contexts
			record.Outcome = answer.Contexts.Outcome.String()
		case answer.Snapshot != nil:
			value = answer.Snapshot
			record.Outcome = answer.Snapshot.Outcome.String()
			record.RevisionAfter = answer.Snapshot.Revision
		case answer.Changes != nil:
			value = answer.Changes
			record.Outcome = answer.Changes.Outcome.String()
			record.RevisionAfter = answer.Changes.Revision
		case answer.Result != nil:
			value = answer.Result
			record.Outcome = answer.Result.Outcome.String()
			record.RevisionAfter = answer.Result.Revision
		}
	}
	var raw []byte
	if value != nil {
		raw, _ = appmodel.ToJSON(value)
	} else if result.Outcome.Known() {
		raw = applications.EncodeCallResult(&result)
	}
	if raw != nil {
		record.ResultDigest = "sha256:" + applicationV3BodyDigest(raw)
	}
}

// The request and owned answer each have a complete 1-MiB frame ceiling.
// Request-derived members occupy at most F decoded bytes together; Reason and
// RevisionAfter can each contribute another F. Subject is at most three UTF-8
// bytes per Windows MAX_LONG_PATH code unit plus its fixed prefix. Regarding
// and emitter program are each bounded at 4096; emitter account/key at 512.
// Record.Encode plus attribution costs at most seven physical bytes per input
// byte (minimal JSON control escaping followed by ordinary JSON encoding).
// 7*(3*F+3*32768+16)+6*(4096+4096+512+512)+16384 < 24 MiB.
// Native ClientLabel is empty. The ordinary sink and history limits stay intact.
const applicationV3AuditPhysicalCap = 24 * 1024 * 1024

// Leave room for two complete frame-derived fields after pre-send admission.
const applicationV3AuditReplyReserve = 7*(2*1024*1024+256) + 16384

func applicationV3AuditWriter(sink logging.Sink, report func(error)) (func(context.Context, appmodel.CallRecord, string) error, func(appmodel.CallRecord, string) error, error) {
	rawSink, ok := sink.(*logging.FileSink)
	if !ok || rawSink == nil {
		return nil, nil, errors.New("application audit: unavailable")
	}
	emitter, err := runtimeLogEmitter()
	if err != nil {
		return nil, nil, err
	}
	exact, err := logging.WithExactEmitter(rawSink, emitter, applicationV3AuditPhysicalCap)
	if err != nil {
		return nil, nil, err
	}
	row := func(record appmodel.CallRecord, regarding string) (logging.Record, error) {
		raw, err := appmodel.ToJSON(&record)
		if err != nil {
			return logging.Record{}, err
		}
		return logging.Record{Time: logging.At(time.Now()), Level: logging.LevelInfo, Msg: "abstraction.facade/appmodel@1", Attrs: map[string]string{"operation": "application.command", "call_record": string(raw), "regarding": regarding}}, nil
	}
	check := func(record appmodel.CallRecord, regarding string) error {
		r, err := row(record, regarding)
		if err != nil {
			return err
		}
		_, err = rawSink.EncodeAttributedExact(r, auditEmitter(emitter, record.Subject), applicationV3AuditPhysicalCap-applicationV3AuditReplyReserve)
		return err
	}
	// Reuse the runtime's existing finite logging writer. A blocked sink keeps
	// its single slot until completion; no second log or receipt store is made.
	writer := &remoteAudit{sink: exact, slot: make(chan struct{}, 1), report: report}
	return func(ctx context.Context, record appmodel.CallRecord, regarding string) error {
		if record.ResultDigest == "" {
			return errors.New("application audit: missing digest")
		}
		r, err := row(record, regarding)
		if err != nil {
			return err
		}
		stamped := auditEmitter(emitter, record.Subject)
		if _, err = rawSink.EncodeAttributedExact(r, stamped, applicationV3AuditPhysicalCap); err != nil {
			return err
		}
		// The proven caller the call was decided for is the record's private
		// subject (LOG-P9), so ReadAboutCaller returns it to that caller.
		about, err := logging.WithExactEmitter(rawSink, stamped, applicationV3AuditPhysicalCap)
		if err != nil {
			return err
		}
		return (&remoteAudit{sink: about, slot: writer.slot, report: writer.report}).write(ctx, r)
	}, check, nil
}

// auditEmitter stamps the runtime's emitter with the proven caller program.
func auditEmitter(e logging.Emitter, subject string) logging.Emitter {
	if identity.ValidSubjectProgram(subject) {
		e.Subject = identity.NormalizeSubjectProgram(subject)
	}
	return e
}
