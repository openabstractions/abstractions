package main

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A process the person runs as administrator on their own account can already
// do everything OA protects, so its requests raise no question (rights
// RIGHTS-R27, owner 2026-11-11). The rights default elevated-callers decides;
// this file binds the caller's peer into each decision's context, and records
// what the default admitted: one runtime log line per admitted action and one
// Activity line per program per runtime session, with no notification.

// The runtime log events the Panel's Activity reads (monitor
// activity_elevated.go).
const (
	elevatedSessionEvent = "rights.elevated.session"
	elevatedAdmitEvent   = "rights.elevated.admit"
)

// elevatedAdmissions records the elevated-callers default's admissions. A nil
// receiver or sink records nothing.
type elevatedAdmissions struct {
	mu   sync.Mutex
	logs logging.Sink
	seen map[string]bool
	now  func() time.Time
}

// useLogs sets the runtime's own attributed log the admissions are written to.
func (r *runtimeRights) useLogs(sink logging.Sink) {
	if r == nil {
		return
	}
	r.elevated.mu.Lock()
	r.elevated.logs = sink
	r.elevated.mu.Unlock()
}

// record writes one log line for an admitted action and, the first time in
// this runtime session for program, the Activity line. A write failure is
// reported and the decision stands.
func (e *elevatedAdmissions) record(subject rwire.Subject, action, resource string, report func(error)) {
	e.mu.Lock()
	sink := e.logs
	first := !e.seen[subject.Program]
	if e.seen == nil {
		e.seen = map[string]bool{}
	}
	if sink != nil {
		e.seen[subject.Program] = true
	}
	now := time.Now
	if e.now != nil {
		now = e.now
	}
	e.mu.Unlock()
	if sink == nil {
		return
	}
	at := logging.At(now())
	if first {
		if err := sink.Write(logging.Record{Schema: logging.SchemaVersion, Time: at, Level: logging.LevelInfo, Msg: "Ran as administrator; OpenAbstractions didn't ask",
			Attrs: map[string]string{"oa_event": elevatedSessionEvent, "program": subject.Program}}); err != nil && report != nil {
			report(err)
		}
	}
	if err := sink.Write(logging.Record{Schema: logging.SchemaVersion, Time: at, Level: logging.LevelInfo, Msg: "Admitted an administrator's request without asking",
		Attrs: map[string]string{"oa_event": elevatedAdmitEvent, "program": subject.Program, "action": action, "resource": resource}}); err != nil && report != nil {
		report(err)
	}
}

// elevatedNote is set in a decision's context; the elevated-callers class
// marks it when it covers the decision.
type elevatedNote struct{ covered atomic.Bool }

type elevatedNoteKey struct{}

func noteElevated(ctx context.Context) {
	if note, ok := ctx.Value(elevatedNoteKey{}).(*elevatedNote); ok {
		note.covered.Store(true)
	}
}

// elevatedCallers is the rights elevated-callers default with this runtime's
// classifier, marking the decision's note when it covers the caller.
func (r *runtimeRights) elevatedCallers() rights.AccountDefault {
	d := rights.ElevatedCallersDefault(func(peer *identity.Peer) bool {
		if r.peerElevated != nil {
			return r.peerElevated(peer)
		}
		return peer.Elevated()
	})
	covers := d.Covers
	d.Covers = func(ctx context.Context, s rwire.Subject, resource string) bool {
		if !covers(ctx, s, resource) {
			return false
		}
		noteElevated(ctx)
		return true
	}
	return d
}

// bindCaller returns ctx carrying the peer a receiving service bound for
// subject, when ctx carries one through inference.WithPeer and none through
// rights.WithBoundPeer, and a fresh note for the elevated class.
func (r *runtimeRights) bindCaller(ctx context.Context, subject rwire.Subject) (context.Context, *elevatedNote) {
	if rights.BoundPeer(ctx) == nil {
		ctx = rights.WithBoundPeer(ctx, boundPeer(ctx, subject))
	}
	note := &elevatedNote{}
	return context.WithValue(ctx, elevatedNoteKey{}, note), note
}

// admitted records an admission the elevated-callers default decided.
func (r *runtimeRights) admitted(note *elevatedNote, permitted bool, subject rwire.Subject, action, resource string) {
	if r == nil || note == nil || !permitted || !note.covered.Load() {
		return
	}
	r.elevated.record(subject, action, resource, r.reportElevated)
}

func (r *runtimeRights) reportElevated(err error) {
	writeHostLogLine(os.Stderr, "runtime: elevated caller log: "+err.Error())
}
