package main

import (
	"context"
	"time"
)

// Architect review 21, decision 2: a sender whose message right to a receiver
// is granted learns why no inbox of that receiver is live. Words follow the
// vocabulary: an announcement ends by withdraw (D46), and a lease nobody
// renewed has lapsed (N8-01).
const (
	applicationPresenceWithdrawn = "withdrawn"
	applicationPresenceLapsed    = "lapsed"
	applicationPresenceEndLimit  = 512
)

// One receiver context's most recent presence end. Codex runs one helper per
// chat, so ends are kept per context rather than per application.
type applicationPresenceEnd struct {
	Kind, Application, Context string
	At                         time.Time
}

func (d *applicationDirectory) recordPresenceEndLocked(l applicationLease, kind string) {
	if d.presenceEnds == nil {
		d.presenceEnds = map[string]applicationPresenceEnd{}
	}
	at := d.now()
	for _, c := range l.value.Contexts {
		d.presenceEnds[l.application+"\x00"+c.Name] = applicationPresenceEnd{Kind: kind, Application: l.application, Context: c.Name, At: at}
		if kind == applicationPresenceLapsed && d.presenceLapsed != nil {
			// Logging I/O never runs under the directory lock.
			go d.presenceLapsed(l.owner.Program, l.application, c.Name, at)
		}
	}
	for len(d.presenceEnds) > applicationPresenceEndLimit {
		oldest := ""
		for key, end := range d.presenceEnds {
			if oldest == "" || end.At.Before(d.presenceEnds[oldest].At) {
				oldest = key
			}
		}
		delete(d.presenceEnds, oldest)
	}
}

// receiverAbsence reports the most recent end among the receiver's contexts,
// or only the named one. No recorded end since this runtime started reads
// receiver_unavailable.
func (d *applicationDirectory) receiverAbsence(ctx context.Context, application, contextName string) string {
	if !d.lock(ctx) {
		return "receiver_unavailable"
	}
	defer d.unlock()
	var latest *applicationPresenceEnd
	for _, end := range d.presenceEnds {
		if end.Application != application || contextName != "" && end.Context != contextName {
			continue
		}
		if latest == nil || end.At.After(latest.At) {
			copied := end
			latest = &copied
		}
	}
	if latest == nil {
		return "receiver_unavailable"
	}
	return "receiver_" + latest.Kind
}

func (d *applicationDirectory) setPresenceLapsed(record func(program, application, contextName string, at time.Time)) {
	d.mu.Lock()
	defer d.unlock()
	d.presenceLapsed = record
}
