package main

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// This private prerequisite owns no message receipts and advertises no service.
// Application registration, trusted agent enrollment and presence stay in d.
type nativeInboxBook struct {
	d             *applicationDirectory
	rows          map[string]*nativeInbox
	cancel        context.CancelFunc
	done          chan struct{}
	closed        bool
	retired       map[string]*nativeInbox
	retiredClaims map[string]bool
}
type nativeInbox struct {
	instance, application, context, status, revision string
	subject                                          rights.Subject
	session                                          string
	process                                          identity.Process
	opened                                           time.Time
	seen                                             time.Time
	pin                                              *identity.Binding
	admission                                        *applicationAgentAdmission
	chatID, chatTitle, sourceInstance, sourceContext string
	sourceOnly                                       bool
	title                                            string // Current presence display claim, captured under the directory lock.
}
type nativeInboxView struct {
	outcome, holder, revision, context string
	opened                             time.Time
	cause                              error
}

var errNativeInboxEvidenceExpired = errors.New("inbox: native evidence expired")
var errNativeInboxEnrollmentDeferred = errors.New("inbox: enrollment check deferred by directory custody")

func (d *applicationDirectory) tryBackgroundAgentAdmission(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !d.mu.TryLock() {
		return errNativeInboxEnrollmentDeferred
	}
	if err := ctx.Err(); err != nil {
		d.unlock()
		return err
	}
	return nil
}

type nativeInboxPeerCapture interface {
	Alive() (bool, error)
	EvidenceValidUntil() time.Time
	Peer() (*identity.Peer, error)
}

// Inspection failures carry no conclusion about the captured occurrence.
// The identity owner's explicit missing/moved/gone results remain final.
type nativeInboxInspectionError struct{ cause error }

func (e *nativeInboxInspectionError) Error() string { return e.cause.Error() }
func (e *nativeInboxInspectionError) Unwrap() error { return e.cause }

func nativeInboxInspectionFailure(err error) error {
	if errors.Is(err, identity.ErrNoBinding) || errors.Is(err, identity.ErrPeerMoved) || errors.Is(err, identity.ErrPeerGone) {
		return err
	}
	return &nativeInboxInspectionError{cause: err}
}

func nativeInboxPeer(pin *identity.Binding, now time.Time) (*identity.Peer, rights.Subject, string, identity.Process, error) {
	if pin == nil {
		return nil, rights.Subject{}, "", identity.Process{}, identity.ErrNoBinding
	}
	return inspectNativeInboxPeer(pin, now)
}

func inspectNativeInboxPeer(pin nativeInboxPeerCapture, now time.Time) (*identity.Peer, rights.Subject, string, identity.Process, error) {
	live, err := pin.Alive()
	if err != nil {
		return nil, rights.Subject{}, "", identity.Process{}, nativeInboxInspectionFailure(err)
	}
	if !live {
		return nil, rights.Subject{}, "", identity.Process{}, identity.ErrPeerGone
	}
	if until := pin.EvidenceValidUntil(); !until.IsZero() && !now.Before(until) {
		return nil, rights.Subject{}, "", identity.Process{}, errNativeInboxEvidenceExpired
	}
	p, err := pin.Peer()
	if err != nil {
		return nil, rights.Subject{}, "", identity.Process{}, nativeInboxInspectionFailure(err)
	}
	if err = p.Check(listen.Program); err != nil {
		return nil, rights.Subject{}, "", identity.Process{}, err
	}
	s, err := credservice.SubjectFromPeer(p)
	if err != nil {
		return nil, rights.Subject{}, "", identity.Process{}, err
	}
	session, ok := applicationSession(p)
	proc, proof := p.Process.Get()
	if !ok || proof < identity.ProofKernel || proc.PID <= 0 || proc.Recycled {
		return nil, rights.Subject{}, "", identity.Process{}, identity.ErrNoBinding
	}
	return p, rights.Subject{Account: s.Account, Program: s.Program}, session, proc, nil
}

func sameInboxProcess(a, b identity.Process) bool {
	return a.PID == b.PID && a.StartTime.Equal(b.StartTime) && a.Generation == b.Generation && !a.Recycled && !b.Recycled
}

// openNativeInbox retains only the binding of the actual admitted request.
// Every failure releases the new pin; an opened row owns its independent pin.
func (d *applicationDirectory) openNativeInbox(ctx context.Context, call *listen.FramedCall, instance, contextName, status string) nativeInboxView {
	return d.openNativeInboxClaim(ctx, call, instance, contextName, status, nil)
}

func (d *applicationDirectory) openNativeInboxClaim(ctx context.Context, call *listen.FramedCall, instance, contextName, status string, claim *msg.ChatBinding, stores ...*messageStore) nativeInboxView {
	if !nativeMessageStatus(status) {
		return nativeInboxView{outcome: "invalid"}
	}
	pin, err := call.RetainProcess()
	if err != nil {
		return nativeInboxView{outcome: "forbidden", cause: err}
	}
	keep := false
	defer func() {
		if !keep {
			pin.Close()
		}
	}()
	p, subject, session, proc, err := nativeInboxPeer(pin, d.now())
	if err != nil || subject.Account != d.owner {
		return nativeInboxView{outcome: "forbidden", cause: err}
	}
	// Look up the application through its actual presence, before consulting the
	// shared enrollment owner. The instance/context never supplies a principal.
	if !d.lock(ctx) {
		return nativeInboxView{outcome: "unavailable", cause: ctx.Err()}
	}
	l, exists := d.instances[instance]
	d.unlock()
	if !exists {
		return nativeInboxView{outcome: "forbidden"}
	}
	view := nativeInboxView{outcome: "forbidden"}
	var admission applicationAgentAdmission
	allowed, err := d.withApplicationAgentCapture(ctx, p, l.application, nil, &admission, func() error {
		if book := d.nativeInboxes; book != nil {
			book.expireRetiredLocked()
		}
		lease, exists := d.instances[instance]
		if !exists || lease.agentAdmission != nil && !sameNativeInboxAdmission(*lease.agentAdmission, admission) {
			return nil
		}
		if d.nativeInboxes != nil && d.nativeInboxes.sourceRetired(subject, proc, contextName) {
			view.outcome = "ended"
			return nil
		}
		targetContext := contextName
		if claim != nil {
			if len(claim.ChatID) > 256 || !utf8.ValidString(claim.ChatID) || !nativeMessageStatus(claim.ChatTitle) {
				view.outcome = "invalid"
				return nil
			}
			for _, ch := range claim.ChatID {
				if unicode.IsControl(ch) {
					view.outcome = "invalid"
					return nil
				}
			}
			if claim.ChatID == "" {
				if claim.ChatTitle != "" || claim.SourceInstance != "" || claim.Launch != nil {
					view.outcome = "invalid"
					return nil
				}
			} else {
				if d.nativeInboxes == nil {
					return nil
				}
				if len(stores) == 0 || stores[0] == nil {
					view.outcome = "unavailable"
					return nil
				}
				var source *nativeInbox
				for _, row := range d.nativeInboxes.rows {
					if row.instance == claim.SourceInstance && row.context == contextName && row.sourceOnly && row.application == l.application && sameInboxProcess(row.process, proc) && row.subject.Account == subject.Account && samePrograms(row.subject.Program, subject.Program) {
						source = row
						break
					}
				}
				if source == nil || source.chatID != "" && source.chatID != claim.ChatID {
					return nil
				}
				sourceLease, sourceLive := d.instances[claim.SourceInstance]
				if !sourceLive || !d.now().Before(sourceLease.deadline) {
					return nil
				}
				source.chatID = claim.ChatID
				canonical, err := stores[0].contextForChat(ctx, subject.Account, admission.Image.Program, claim.ChatID, claim)
				if err != nil {
					view.outcome, view.cause = "unavailable", err
					return nil
				}
				view.context = canonical
				declared := false
				for _, c := range lease.value.Contexts {
					if c.Name == canonical {
						declared = true
					}
				}
				if !declared {
					view.outcome = "page"
					return nil
				}
				targetContext = canonical
			}
		}
		var previous *nativeInbox
		key := l.application + "\x00" + targetContext
		if d.nativeInboxes != nil {
			previous = d.nativeInboxes.rows[key]
		}
		view, keep = d.acquireNativeInboxLocked(pin, subject, session, proc, instance, targetContext, status, nativeChatClaim{claim, contextName})
		if claim != nil && claim.ChatID != "" {
			view.context = targetContext
		}
		if view.outcome == "opened" && lease.agentAdmission == nil {
			lease.agentAdmission = &admission
			d.instances[instance] = lease
		}
		if view.outcome == "opened" {
			row := d.nativeInboxes.rows[key]
			row.admission = lease.agentAdmission
			// Startup readiness waits on the directory's existing change signal.
			// Publish after the authenticated inbox admission is attached.
			d.notifyDirectoryLocked()
			if previous == nil || previous.instance != row.instance || previous.revision != row.revision {
				d.recheckMessagesLocked()
			}
		}
		return nil
	})
	if err != nil {
		return nativeInboxView{outcome: "unavailable", cause: err}
	}
	if !allowed {
		return nativeInboxView{outcome: "forbidden"}
	}
	return view
}

func sameNativeInboxAdmission(a, b applicationAgentAdmission) bool {
	return a.Enrollment.Application == b.Enrollment.Application && a.Enrollment.Revision == b.Enrollment.Revision &&
		a.Enrollment.State == b.Enrollment.State && a.Enrollment.Program == b.Enrollment.Program &&
		a.Image.Program == b.Image.Program && a.Image.Key == b.Image.Key && a.Image.Binding == b.Image.Binding && a.Image.Publisher == b.Image.Publisher
}

type nativeChatClaim struct {
	binding       *msg.ChatBinding
	sourceContext string
}

func (d *applicationDirectory) acquireNativeInboxLocked(pin *identity.Binding, subject rights.Subject, session string, proc identity.Process, instance, contextName, status string, claims ...nativeChatClaim) (nativeInboxView, bool) {
	var claim *msg.ChatBinding
	var sourceContext string
	if len(claims) != 0 {
		claim, sourceContext = claims[0].binding, claims[0].sourceContext
	}
	if d.nativeInboxesClosed {
		return nativeInboxView{outcome: "unavailable"}, false
	}
	d.expireLocked()
	l, exists := d.instances[instance]
	if !exists || l.owner.Account != subject.Account || !samePrograms(l.owner.Program, subject.Program) || l.session != session {
		return nativeInboxView{outcome: "forbidden"}, false
	}
	found := false
	for _, c := range l.value.Contexts {
		if c.Name == contextName {
			found = true
			break
		}
	}
	if !found || contextName == "" {
		return nativeInboxView{outcome: "forbidden"}, false
	}
	if _, _, _, _, err := nativeInboxPeer(pin, d.now()); err != nil {
		return nativeInboxView{outcome: "forbidden"}, false
	}
	b := d.nativeInboxes
	if b == nil {
		watchCtx, cancel := context.WithCancel(context.Background())
		b = &nativeInboxBook{d: d, rows: map[string]*nativeInbox{}, cancel: cancel, done: make(chan struct{})}
		d.nativeInboxes = b
		go b.watch(watchCtx)
	}
	if b.closed {
		return nativeInboxView{outcome: "unavailable"}, false
	}
	if b.retiredClaims[instance+"\x00"+contextName] || b.sourceRetired(subject, proc, contextName) {
		return nativeInboxView{outcome: "ended"}, false
	}
	key := l.application + "\x00" + contextName
	var old *nativeInbox
	for _, row := range b.rows {
		if row.context == contextName && samePrograms(row.subject.Program, subject.Program) {
			old = row
			break
		}
	}
	if old != nil && old.instance != instance && old.application == l.application && sameInboxProcess(old.process, proc) && old.session == session && (claim == nil || claim.ChatID == "") {
		// The same native process re-admits its own context under the new
		// instance it just announced, as after a missed renewal. Its older
		// instance's row would otherwise hold the context until that lease
		// lapses, keeping the helper's tools unavailable for up to a minute
		// (owner, R20c 2026-11-10). Another process still finds it held.
		old.pin.Close()
		delete(b.rows, old.application+"\x00"+old.context)
		old = nil
	}
	if old != nil {
		if old.application != l.application || old.instance != instance || !sameInboxProcess(old.process, proc) {
			if claim == nil || claim.ChatID == "" || old.chatID != claim.ChatID || old.sourceOnly {
				return nativeInboxView{outcome: "held", holder: "app:" + old.application, opened: old.opened}, false
			}
			// Fence this chat claim and its original native source occurrence.
			// Other contexts advertised by the same presence remain independent.
			var retiredSource *nativeInbox
			for _, source := range b.rows {
				if source.sourceOnly && source.instance == old.sourceInstance && source.context == old.sourceContext && (!sameInboxProcess(source.process, proc) || source.context != sourceContext) {
					retained, err := source.pin.RetainProcess()
					if err != nil {
						return nativeInboxView{outcome: "unavailable", cause: err}, false
					}
					copy := *source
					copy.pin = retained
					retiredSource = &copy
					break
				}
			}
			if b.retiredClaims == nil {
				b.retiredClaims = map[string]bool{}
			}
			b.retiredClaims[old.instance+"\x00"+old.context] = true
			if retiredSource != nil {
				if b.retired == nil {
					b.retired = map[string]*nativeInbox{}
				}
				retiredKey := retiredSource.instance + "\x00" + retiredSource.context
				if prior := b.retired[retiredKey]; prior != nil {
					prior.pin.Close()
				}
				b.retired[retiredKey] = retiredSource
			}
			old.pin.Close()
			delete(b.rows, old.application+"\x00"+old.context)
			old = nil
		}
	}
	if old != nil {
		renewed := *old
		renewed.seen = d.now()
		if claim != nil {
			renewed.chatID, renewed.chatTitle, renewed.sourceInstance = claim.ChatID, nativeMessageHeader(claim.ChatTitle), claim.SourceInstance
			renewed.sourceContext = sourceContext
			if claim.ChatID == "" && old.sourceOnly {
				renewed.chatID = old.chatID
				renewed.sourceContext = ""
			}
			renewed.sourceOnly = claim.ChatID == ""
		}
		if old.status != status || claim != nil && old.chatTitle != nativeMessageHeader(claim.ChatTitle) {
			revision, err := applicationToken()
			if err != nil {
				return nativeInboxView{outcome: "unavailable"}, false
			}
			renewed.status, renewed.revision = status, revision
		}
		// Replace the immutable occurrence under the directory lock. A watcher
		// holding the prior row cannot delete this freshly authenticated renewal.
		renewed.pin = pin
		b.rows[key] = &renewed
		old.pin.Close()
		return nativeInboxView{outcome: "opened", revision: renewed.revision, opened: renewed.opened}, true
	}
	revision, err := applicationToken()
	if err != nil {
		return nativeInboxView{outcome: "unavailable"}, false
	}
	row := &nativeInbox{instance: instance, application: l.application, context: contextName, status: status, revision: revision, subject: subject, session: session, process: proc, opened: d.now(), seen: d.now(), pin: pin}
	if claim != nil {
		row.chatID, row.chatTitle, row.sourceInstance = claim.ChatID, nativeMessageHeader(claim.ChatTitle), claim.SourceInstance
		row.sourceContext = sourceContext
		if claim.ChatID == "" {
			row.sourceContext = ""
		}
		row.sourceOnly = claim.ChatID == ""
	}
	b.rows[key] = row
	return nativeInboxView{outcome: "opened", revision: revision, opened: row.opened}, true
}

// checkNativeInboxOwner preserves the private prerequisite's ownership probe.
func (d *applicationDirectory) checkNativeInboxOwner(ctx context.Context, call *listen.FramedCall, instance, contextName string) bool {
	owned, err := d.withNativeInboxOwner(ctx, call, instance, contextName, nil)
	return err == nil && owned
}

// withNativeInboxOwner keeps the admitted occurrence and current enrollment
// held through consume. The callback runs under the existing directory and
// registration-storage locks; it must not reenter that owner or wait for work.
func (d *applicationDirectory) withNativeInboxOwner(ctx context.Context, call *listen.FramedCall, instance, contextName string, consume func(*nativeInbox) error) (bool, error) {
	return d.withNativeMessageOwner(ctx, call, instance, contextName, nil, consume)
}

func (d *applicationDirectory) withNativeMessageOwner(ctx context.Context, call *listen.FramedCall, instance, contextName string, related []applications.AgentEnrollment, consume func(*nativeInbox) error) (bool, error) {
	return d.withNativeMessageOwnerInfo(ctx, call, instance, contextName, related, nil, consume)
}

func (d *applicationDirectory) withNativeMessageOwnerInfo(ctx context.Context, call *listen.FramedCall, instance, contextName string, related []applications.AgentEnrollment, capture *applicationAgentAdmission, consume func(*nativeInbox) error) (bool, error) {
	pin, err := call.RetainProcess()
	if err != nil {
		return false, err
	}
	defer pin.Close()
	p, subject, session, proc, err := nativeInboxPeer(pin, d.now())
	if err != nil || subject.Account != d.owner {
		return false, identity.ErrNoBinding
	}
	if !d.lock(ctx) {
		return false, ctx.Err()
	}
	l, ok := d.instances[instance]
	d.unlock()
	if !ok {
		return false, nil
	}
	owned := false
	if capture == nil {
		capture = new(applicationAgentAdmission)
	}
	_, err = d.withApplicationAgentAdmission(ctx, p, l.application, related, capture, func() error {
		row, err := d.nativeMessageOwnerLocked(pin, instance, contextName, l.application, subject, session, proc, capture)
		if err != nil {
			return err
		}
		owned = row != nil
		if owned && consume != nil {
			return consume(row)
		}
		return nil
	}, len(related) > 0)
	return err == nil && owned, err
}

// nativeMessageOwnerLocked checks the exact sender occurrence without
// updating inbox or presence state. Shared directory custody is sufficient.
func (d *applicationDirectory) nativeMessageOwnerLocked(pin *identity.Binding, instance, contextName, application string, subject rights.Subject, session string, proc identity.Process, capture *applicationAgentAdmission) (*nativeInbox, error) {
	lease, exists := d.instances[instance]
	if !exists || lease.agentAdmission != nil && !sameNativeInboxAdmission(*lease.agentAdmission, *capture) {
		return nil, nil
	}
	now := d.now()
	_, currentSubject, currentSession, currentProcess, err := nativeInboxPeer(pin, now)
	if err != nil {
		return nil, err
	}
	if currentSubject.Account != subject.Account || !samePrograms(currentSubject.Program, subject.Program) ||
		currentSession != session || !sameInboxProcess(currentProcess, proc) {
		return nil, nil
	}
	b := d.nativeInboxes
	if b == nil || b.closed || b.retiredClaims[instance+"\x00"+contextName] || b.sourceRetired(subject, proc, contextName) {
		return nil, nil
	}
	row := b.rows[application+"\x00"+contextName]
	if row == nil || row.instance != instance || row.subject.Account != subject.Account || !samePrograms(row.subject.Program, subject.Program) ||
		row.session != session || !sameInboxProcess(row.process, proc) || !d.nativeInboxLiveLocked(row, now) {
		return nil, nil
	}
	return row, nil
}

// nativeInboxLiveLocked checks the inbox's own lease and retained evidence.
// Both admission and the presence sweep use this check under directory custody.
func (d *applicationDirectory) nativeInboxLiveLocked(row *nativeInbox, now time.Time) bool {
	lease, ok := d.instances[row.instance]
	if !ok || !now.Before(lease.deadline) || lease.application != row.application || lease.session != row.session || lease.owner.Account != row.subject.Account || !samePrograms(lease.owner.Program, row.subject.Program) {
		return false
	}
	for _, context := range lease.value.Contexts {
		if context.Name == row.context {
			_, _, _, _, err := nativeInboxPeer(row.pin, now)
			return err == nil
		}
	}
	return false
}

// expireNativeInboxesLocked is called by the presence owner's expiry/withdrawal.
// Liveness and evidence age are separate from historical Peer validity.
func (d *applicationDirectory) expireNativeInboxesLocked() {
	b := d.nativeInboxes
	if b == nil {
		return
	}
	b.expireRetiredLocked()
	now := d.now()
	for key, row := range b.rows {
		if !d.nativeInboxLiveLocked(row, now) {
			row.pin.Close()
			delete(b.rows, key)
			d.recheckMessagesLocked()
		}
	}
}

func (b *nativeInboxBook) sourceRetired(subject rights.Subject, proc identity.Process, contextName string) bool {
	for _, source := range b.retired {
		if source.context == contextName && source.subject.Account == subject.Account && samePrograms(source.subject.Program, subject.Program) && sameInboxProcess(source.process, proc) {
			return true
		}
	}
	return false
}

// A presence renewal cannot erase a retired source lifetime. Its retained
// native process capture owns the fence until that exact process is gone.
func (b *nativeInboxBook) expireRetiredLocked() {
	for key, source := range b.retired {
		if retiredNativeInboxGone(source) {
			source.pin.Close()
			delete(b.retired, key)
		}
	}
	for key := range b.retiredClaims {
		instance, _, _ := strings.Cut(key, "\x00")
		if _, live := b.d.instances[instance]; !live {
			delete(b.retiredClaims, key)
		}
	}
}

func retiredNativeInboxGone(source *nativeInbox) bool {
	alive, err := source.pin.Alive()
	return err == nil && !alive || errors.Is(err, identity.ErrPeerGone) || errors.Is(err, identity.ErrPeerMoved)
}

func (b *nativeInboxBook) hasExpiredLocked(now time.Time) bool {
	for _, lease := range b.d.instances {
		if !now.Before(lease.deadline) {
			return true
		}
	}
	for _, row := range b.rows {
		if !b.d.nativeInboxLiveLocked(row, now) {
			return true
		}
	}
	for _, source := range b.retired {
		if retiredNativeInboxGone(source) {
			return true
		}
	}
	for key := range b.retiredClaims {
		instance, _, _ := strings.Cut(key, "\x00")
		if _, live := b.d.instances[instance]; !live {
			return true
		}
	}
	return false
}

// Healthy sweeps share publication custody. Expiry repeats its checks after
// acquiring write custody because an observed lease may have been renewed.
func (b *nativeInboxBook) sweep(ctx context.Context) bool {
	if !b.d.lockRead(ctx) {
		return false
	}
	expired := b.hasExpiredLocked(b.d.now())
	b.d.mu.RUnlock()
	if !expired {
		return true
	}
	if !b.d.lock(ctx) {
		return false
	}
	b.d.expireLocked()
	b.d.unlock()
	return true
}

// Native inbox watch cadences. Every liveness tick retires the rows whose
// process ended, whose presence lapsed or whose evidence expired; this check
// reads only the retained process handles. Every enrollment pass rechecks each
// row's enrollment, image and person answer through the admission owner. That
// check reads the registration store under the directory lock every presence
// renewal also needs. One pass checks each distinct application and native
// program once, however many chats it keeps open. Every message operation and
// every presence renewal still rechecks its own enrollment.
const (
	nativeInboxLivenessTick   = 100 * time.Millisecond
	nativeInboxEnrollmentTick = time.Second
)

// Check enrollment outside the directory mutex: its one owner takes that lock
// and validates the current image and actual person occurrence itself.
func (b *nativeInboxBook) watch(ctx context.Context) {
	defer close(b.done)
	ticker := time.NewTicker(nativeInboxLivenessTick)
	defer ticker.Stop()
	var checked time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !b.sweep(ctx) {
			return
		}
		if now := time.Now(); now.Sub(checked) >= nativeInboxEnrollmentTick {
			if b.checkEnrollment(ctx) {
				checked = now
			}
		}
	}
}

type nativeInboxAdmissionCheck struct {
	admission applicationAgentAdmission
	allowed   bool
	err       error
}

// checkEnrollment captures the current admission once per application and
// native program subject; rows sharing both share the owner's answer. Each row
// still answers for its own process capture and presence lease.
func (b *nativeInboxBook) checkEnrollment(ctx context.Context) bool {
	if !b.d.lockRead(ctx) {
		return false
	}
	rows := make(map[string]*nativeInbox, len(b.rows))
	for key, row := range b.rows {
		rows[key] = row
	}
	b.d.mu.RUnlock()
	checks := map[string]nativeInboxAdmissionCheck{}
	for key, row := range rows {
		p, err := row.pin.Peer()
		var check nativeInboxAdmissionCheck
		if err == nil {
			group := row.application + "\x00" + row.subject.Account + "\x00" + row.subject.Program
			var seen bool
			if check, seen = checks[group]; !seen {
				check.allowed, check.err = b.d.withApplicationAgentAdmissionLock(ctx, p, row.application, nil, &check.admission, nil, false, b.d.tryBackgroundAgentAdmission)
				if errors.Is(check.err, errNativeInboxEnrollmentDeferred) {
					return false
				}
				checks[group] = check
			}
		} else {
			check.err = err
		}
		if !b.d.lockRead(ctx) {
			return false
		}
		current := b.rows[key] == row
		healthy := b.matchesEnrollmentLocked(row, check)
		b.d.mu.RUnlock()
		if !current || healthy {
			continue
		}
		if !b.d.lock(ctx) {
			return false
		}
		if !b.matchesEnrollmentLocked(row, check) {
			b.retireLocked(key, row)
		}
		b.d.unlock()
	}
	return true
}

func (b *nativeInboxBook) matchesEnrollmentLocked(row *nativeInbox, check nativeInboxAdmissionCheck) bool {
	lease, exists := b.d.instances[row.instance]
	return check.err == nil && check.allowed && exists &&
		(lease.agentAdmission == nil || sameNativeInboxAdmission(*lease.agentAdmission, check.admission))
}

// A late observer only retires the exact occurrence it checked.
func (b *nativeInboxBook) retireLocked(key string, row *nativeInbox) {
	if b.rows[key] == row {
		row.pin.Close()
		delete(b.rows, key)
		b.d.recheckMessagesLocked()
	}
}

// snapshotLocked gives unlocked readers independent custody of each capture.
// Renewal can release its own pin without retiring a reader's snapshot.
func (b *nativeInboxBook) snapshotLocked(application string) ([]nativeInbox, error) {
	var rows []nativeInbox
	for _, row := range b.rows {
		if application != "" && row.application != application {
			continue
		}
		presence, ok := b.d.instances[row.instance]
		if !ok || !b.d.now().Before(presence.deadline) {
			continue
		}
		pin, err := row.pin.RetainProcess()
		if err != nil {
			closeNativeInboxSnapshots(rows)
			return nil, err
		}
		copy := *row
		copy.pin = pin
		rows = append(rows, copy)
	}
	return rows, nil
}

func closeNativeInboxSnapshots(rows []nativeInbox) {
	for _, row := range rows {
		row.pin.Close()
	}
}

func (d *applicationDirectory) closeNativeInboxes() {
	d.mu.Lock()
	d.nativeInboxesClosed = true
	b := d.nativeInboxes
	if b != nil && !b.closed {
		b.closed = true
		b.cancel()
		for key, source := range b.retired {
			source.pin.Close()
			delete(b.retired, key)
		}
		for key, row := range b.rows {
			row.pin.Close()
			delete(b.rows, key)
		}
	}
	d.unlock()
	if b != nil {
		<-b.done
	}
}

// Pure private text profile; raw values remain the routing/replay authority.
func nativeMessageText(subject, body, key string) bool {
	return utf8.ValidString(subject) && utf8.RuneCountInString(subject) <= 80 && !strings.ContainsAny(subject, "\r\n\u2028\u2029") && utf8.ValidString(body) && len(body) <= 8192 && utf8.ValidString(key) && len(key) >= 1 && len(key) <= 128
}
func nativeMessageStatus(status string) bool {
	return utf8.ValidString(status) && utf8.RuneCountInString(status) <= 200
}
func nativeMessageHeader(value string) string {
	var out []rune
	for _, r := range value {
		if !unicode.IsControl(r) && r != '\u2028' && r != '\u2029' && !(r >= '\u202a' && r <= '\u202e') && !(r >= '\u2066' && r <= '\u2069') {
			out = append(out, r)
		}
	}
	return string(out)
}
