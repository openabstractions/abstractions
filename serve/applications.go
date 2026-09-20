package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	cas "github.com/openabstractions/abstraction-cas/go/api"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

const (
	ActionApplicationManage   = "abstraction.facade/application.manage"
	ActionApplicationRead     = "abstraction.facade/application.read"
	ActionApplicationAnnounce = "abstraction.facade/application.announce"
	ActionApplicationActivate = "abstraction.facade/application.activate"
	ApplicationsContract      = "abstraction.facade/applications@1"
)

type applicationLease struct {
	application string
	owner       rights.Subject
	session     string
	value       wire.ApplicationInstance
	deadline    time.Time
}

type applicationActivationAttempt struct {
	done       chan struct{}
	result     wire.ApplicationActivationResult
	completed  bool
	generation uint64
	session    string
}

type applicationDirectory struct {
	mu          sync.Mutex
	dir, epoch  string
	base        cas.Value
	store       cas.BoundedFileStore
	owner       string
	descriptors map[string]wire.ApplicationDescriptor
	instances   map[string]applicationLease
	attempts    map[string]*applicationActivationAttempt
	generations map[string]uint64
	changed     chan struct{}
	now         func() time.Time
	decide      func(context.Context, rights.Subject, string, string) (bool, error)
	launch      func(string, []string) error
	stat        func(string) (os.FileInfo, error)
}

func applicationToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func openApplications(state, owner string, decide func(context.Context, rights.Subject, string, string) (bool, error)) (*applicationDirectory, error) {
	if state == "" || owner == "" || decide == nil {
		return nil, errors.New("applications: state, owner and policy required")
	}
	epoch, err := applicationToken()
	if err != nil {
		return nil, err
	}
	d := &applicationDirectory{dir: filepath.Join(state, "applications"), owner: owner, epoch: epoch,
		descriptors: map[string]wire.ApplicationDescriptor{}, instances: map[string]applicationLease{}, attempts: map[string]*applicationActivationAttempt{}, generations: map[string]uint64{}, changed: make(chan struct{}), now: time.Now, decide: decide, launch: launchApplication, stat: os.Stat}
	if err := os.MkdirAll(d.dir, 0700); err != nil {
		return nil, err
	}
	d.store = cas.BoundedFileStore{MaxBytes: 600 * 1024}
	d.base, err = d.store.Read(filepath.Join(d.dir, "descriptors.json"))
	if err != nil {
		return nil, err
	}
	if d.base.Data != nil {
		if json.Unmarshal(d.base.Data, &d.descriptors) != nil || d.descriptors == nil || len(d.descriptors) > 64 {
			return nil, errors.New("applications: invalid descriptors")
		}
		for name, descriptor := range d.descriptors {
			if name != descriptor.Name || !validApplicationDescriptor(descriptor) {
				return nil, errors.New("applications: invalid descriptor")
			}
		}
	}

	return d, nil
}

func applicationEncodedFits(v any, limit int) bool {
	raw, err := json.Marshal(v)
	return err == nil && len(raw) <= limit
}

func validApplicationDescriptor(v wire.ApplicationDescriptor) bool {
	if !applicationEncodedFits(v, 8192) || !providerName.MatchString(v.Name) || !filepath.IsAbs(v.Program) || filepath.Clean(v.Program) != v.Program || len(v.Program) > 4096 || strings.ContainsRune(v.Program, 0) || len(v.Title) == 0 || len(v.Title) > 256 || len(v.StartGuidance) > 4096 {
		return false
	}
	if v.Activation == nil {
		return true
	}
	r := v.Activation
	if len(r.Arguments) > 64 || r.ReadinessTimeoutMs < 100 || r.ReadinessTimeoutMs > 30000 || !validApplicationInterface(r.Readiness) {
		return false
	}
	for _, argument := range r.Arguments {
		if len(argument) == 0 || len(argument) > 4096 || strings.ContainsRune(argument, 0) {
			return false
		}
	}
	return true
}

func validApplicationInterface(v wire.ApplicationInterface) bool {
	return providerName.MatchString(v.Name) && len(v.Protocol) > 0 && len(v.Protocol) <= 128 && len(v.Contract) <= 256
}

func (d *applicationDirectory) authorized(ctx context.Context, s rights.Subject, action, resource string) wire.ApplicationOutcome {
	if ctx.Err() != nil {
		return wire.ApplicationOutcomeUnavailable
	}
	if s.Account != d.owner || s.Program == "" {
		return wire.ApplicationOutcomeForbidden
	}
	ok, err := d.decide(ctx, s, action, resource)
	if err != nil || ctx.Err() != nil {
		return wire.ApplicationOutcomeUnavailable
	}
	if !ok {
		return wire.ApplicationOutcomeForbidden
	}
	return wire.ApplicationOutcomeApplied
}

// lock permits a caller to leave while a durable mutation owns the directory.
func (d *applicationDirectory) lock(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		if d.mu.TryLock() {
			if ctx.Err() != nil {
				d.mu.Unlock()
				return false
			}
			return true
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
func appChange(outcome wire.ApplicationOutcome, reason string) wire.ApplicationChange {
	return wire.ApplicationChange{Outcome: outcome, Reason: reason}
}
func (d *applicationDirectory) notifyLocked() { close(d.changed); d.changed = make(chan struct{}) }

func (d *applicationDirectory) register(ctx context.Context, s rights.Subject, v wire.ApplicationDescriptor) wire.ApplicationChange {
	if out := d.authorized(ctx, s, ActionApplicationManage, "account"); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if !validApplicationDescriptor(v) || samePrograms(s.Program, v.Program) {
		return appChange(wire.ApplicationOutcomeInvalid, "descriptor")
	}
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	defer d.mu.Unlock()
	if _, ok := d.descriptors[v.Name]; ok {
		return appChange(wire.ApplicationOutcomeConflict, "name")
	}
	if len(d.descriptors) >= 64 {
		return appChange(wire.ApplicationOutcomeUnavailable, "capacity")
	}
	next := maps.Clone(d.descriptors)
	next[v.Name] = v
	if err := d.persistLocked(ctx, next); err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
	}

	d.notifyLocked()
	d.generations[v.Name]++
	return appChange(wire.ApplicationOutcomeApplied, "")
}
func (d *applicationDirectory) remove(ctx context.Context, s rights.Subject, name string) wire.ApplicationChange {
	if out := d.authorized(ctx, s, ActionApplicationManage, "account"); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if !providerName.MatchString(name) {
		return appChange(wire.ApplicationOutcomeInvalid, "application")
	}
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	defer d.mu.Unlock()
	if _, ok := d.descriptors[name]; !ok {
		return appChange(wire.ApplicationOutcomeUnknown, "")
	}
	next := maps.Clone(d.descriptors)
	delete(next, name)
	if err := d.persistLocked(ctx, next); err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
	}

	for key, l := range d.instances {
		if l.application == name {
			delete(d.instances, key)
		}
	}
	if attempt := d.attempts[name]; attempt != nil {
		d.generations[name]++
		result := activationResult(wire.ApplicationActivationOutcomeDisabled, "descriptor_removed")
		if attempt.completed {
			tombstone := &applicationActivationAttempt{done: make(chan struct{}), result: result, completed: true, generation: attempt.generation, session: attempt.session}
			close(tombstone.done)
			d.attempts[name] = tombstone
		} else {
			d.completeActivationLocked(name, attempt, result, true)
		}
	} else {
		delete(d.generations, name)
	}
	d.notifyLocked()
	return appChange(wire.ApplicationOutcomeApplied, "")
}

// persistLocked uses the shared CAS provider for synced atomic replacement.
// Another writer changing the same root is refused instead of losing its update.
func (d *applicationDirectory) persistLocked(ctx context.Context, next map[string]wire.ApplicationDescriptor) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = d.store.WriteContext(ctx, filepath.Join(d.dir, "descriptors.json"), d.base, raw); err != nil {
		return err
	}
	d.base = cas.Value{Data: raw}
	d.descriptors = next
	return nil
}
func validApplicationPresence(p wire.ApplicationPresence) bool {
	if !applicationEncodedFits(p, 2048) || !providerName.MatchString(p.Application) || len(p.Instance) > 128 || p.LeaseMs < 1000 || p.LeaseMs > 60000 || len(p.Interfaces) > 16 || len(p.Contexts) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range p.Interfaces {
		if !validApplicationInterface(v) || seen[v.Name] {
			return false
		}
		seen[v.Name] = true
	}
	clear(seen)
	for _, v := range p.Contexts {
		if !providerName.MatchString(v.Name) || seen[v.Name] || len(v.Title) > 256 || len(v.Revision) > 256 {
			return false
		}
		seen[v.Name] = true
	}
	return true
}
func (d *applicationDirectory) expireLocked() {
	now := d.now()
	for id, l := range d.instances {
		if !now.Before(l.deadline) {
			delete(d.instances, id)
		}
	}
}
func (d *applicationDirectory) announce(ctx context.Context, s rights.Subject, p wire.ApplicationPresence) wire.ApplicationChange {
	return d.announceInSession(ctx, s, "test", p)
}

func (d *applicationDirectory) announceInSession(ctx context.Context, s rights.Subject, session string, p wire.ApplicationPresence) wire.ApplicationChange {
	if !validApplicationPresence(p) {
		return appChange(wire.ApplicationOutcomeInvalid, "presence")
	}
	if session == "" {
		return appChange(wire.ApplicationOutcomeForbidden, "")
	}
	if out := d.authorized(ctx, s, ActionApplicationAnnounce, "app:"+p.Application); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	defer d.mu.Unlock()
	d.expireLocked()
	v, ok := d.descriptors[p.Application]
	if !ok {
		return appChange(wire.ApplicationOutcomeUnknown, "")
	}
	if !samePrograms(v.Program, s.Program) {
		if attempt := d.attempts[p.Application]; attempt != nil {
			d.completeActivationLocked(p.Application, attempt, activationResult(wire.ApplicationActivationOutcomeIdentityRefused, "program"), true)
		}
		return appChange(wire.ApplicationOutcomeForbidden, "")
	}
	id := p.Instance
	if id != "" {
		old, ok := d.instances[id]
		if !ok {
			return appChange(wire.ApplicationOutcomeStale, "")
		}
		if old.application != p.Application || old.owner.Account != s.Account || !samePrograms(old.owner.Program, s.Program) || old.session != session {
			return appChange(wire.ApplicationOutcomeForbidden, "")
		}
	} else {
		if len(d.instances) >= 128 {
			return appChange(wire.ApplicationOutcomeUnavailable, "capacity")
		}
		token, err := applicationToken()
		if err != nil {
			return appChange(wire.ApplicationOutcomeUnavailable, "")
		}
		id = d.epoch + "." + token
	}
	interfaces := append([]wire.ApplicationInterface{}, p.Interfaces...)
	contexts := append([]wire.ApplicationContext{}, p.Contexts...)
	sort.Slice(interfaces, func(i, j int) bool { return interfaces[i].Name < interfaces[j].Name })
	sort.Slice(contexts, func(i, j int) bool { return contexts[i].Name < contexts[j].Name })
	deadline := d.now().Add(time.Duration(p.LeaseMs) * time.Millisecond)
	d.instances[id] = applicationLease{application: p.Application, owner: s, session: session, value: wire.ApplicationInstance{Instance: id, Interfaces: interfaces, Contexts: contexts, ExpiresUnixMs: deadline.UnixMilli()}, deadline: deadline}
	d.notifyLocked()
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: id}
}

func activationResult(outcome wire.ApplicationActivationOutcome, reason string) wire.ApplicationActivationResult {
	return wire.ApplicationActivationResult{Outcome: outcome, Reason: reason}
}

func (d *applicationDirectory) readyInstanceLocked(application string, readiness wire.ApplicationInterface, session string) string {
	d.expireLocked()
	for _, lease := range d.instances {
		if lease.application != application || lease.session != session {
			continue
		}
		for _, offered := range lease.value.Interfaces {
			if offered == readiness {
				return lease.value.Instance
			}
		}
	}
	return ""
}

func (d *applicationDirectory) completeActivationLocked(application string, attempt *applicationActivationAttempt, result wire.ApplicationActivationResult, retain bool) {
	if d.attempts[application] != attempt || attempt.completed {
		return
	}
	attempt.result, attempt.completed = result, true
	if !retain {
		delete(d.attempts, application)
	}
	close(attempt.done)
}

func (d *applicationDirectory) finishActivation(application string, attempt *applicationActivationAttempt, result wire.ApplicationActivationResult, retain bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.completeActivationLocked(application, attempt, result, retain)
}

func (d *applicationDirectory) runActivation(application string, descriptor wire.ApplicationDescriptor, attempt *applicationActivationAttempt) {
	timer := time.NewTimer(time.Duration(descriptor.Activation.ReadinessTimeoutMs) * time.Millisecond)
	defer timer.Stop()
	type statResult struct {
		info os.FileInfo
		err  error
	}
	inspected := make(chan statResult, 1)
	go func() {
		info, err := d.stat(descriptor.Program)
		inspected <- statResult{info: info, err: err}
	}()
	select {
	case result := <-inspected:
		if result.err != nil || result.info == nil || !result.info.Mode().IsRegular() {
			d.finishActivation(application, attempt, activationResult(wire.ApplicationActivationOutcomeUnknown, "not_installed"), false)
			return
		}
	case <-timer.C:
		// Inspection has no side effect, but retaining this terminal attempt
		// bounds both retry pressure and blocked inspection goroutines.
		d.finishActivation(application, attempt, activationResult(wire.ApplicationActivationOutcomeUnavailable, "identity_timeout"), true)
		return
	}
	d.mu.Lock()
	if d.attempts[application] != attempt || attempt.completed || d.generations[application] != attempt.generation {
		d.mu.Unlock()
		return
	}
	// Issuing the launch while this generation is current is the activation's
	// linearization point. The platform call runs outside the directory lock so
	// a stalled desktop service cannot block removal, observation or presence.
	launched := make(chan error, 1)
	go func() {
		launched <- d.launch(descriptor.Program, append([]string{}, descriptor.Activation.Arguments...))
	}()
	d.mu.Unlock()
	select {
	case err := <-launched:
		if err != nil {
			d.finishActivation(application, attempt, activationResult(wire.ApplicationActivationOutcomeLaunchRefused, "launch"), false)
			return
		}
	case <-timer.C:
		// The platform may still complete the issued start. Retain uncertainty
		// and never signal a process whose ownership remains with the user.
		d.finishActivation(application, attempt, activationResult(wire.ApplicationActivationOutcomeUnavailable, "launch_timeout"), true)
		return
	}
	for {
		d.mu.Lock()
		if d.attempts[application] != attempt || attempt.completed || d.generations[application] != attempt.generation {
			d.mu.Unlock()
			return
		}
		if instance := d.readyInstanceLocked(application, descriptor.Activation.Readiness, attempt.session); instance != "" {
			d.mu.Unlock()
			d.finishActivation(application, attempt, wire.ApplicationActivationResult{Outcome: wire.ApplicationActivationOutcomeReady, Instance: instance, Started: true}, false)
			return
		}
		changed := d.changed
		d.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			result := activationResult(wire.ApplicationActivationOutcomeNotReady, "readiness_timeout")
			result.Started = true
			// A successful process start with no verified presence has an unknown
			// lifetime. Retain this terminal attempt to prevent a later request
			// from silently starting a duplicate process.
			d.finishActivation(application, attempt, result, true)
			return
		}
	}
}

func (d *applicationDirectory) activate(ctx context.Context, s rights.Subject, application string) wire.ApplicationActivationResult {
	return d.activateInSession(ctx, s, "test", application)
}

func (d *applicationDirectory) activateInSession(ctx context.Context, s rights.Subject, session, application string) wire.ApplicationActivationResult {
	if !providerName.MatchString(application) {
		return activationResult(wire.ApplicationActivationOutcomeInvalid, "application")
	}
	if session == "" {
		return activationResult(wire.ApplicationActivationOutcomeForbidden, "session")
	}
	if out := d.authorized(ctx, s, ActionApplicationActivate, "app:"+application); out != wire.ApplicationOutcomeApplied {
		if out == wire.ApplicationOutcomeForbidden {
			return activationResult(wire.ApplicationActivationOutcomeForbidden, "")
		}
		return activationResult(wire.ApplicationActivationOutcomeUnavailable, "")
	}
	if !d.lock(ctx) {
		return activationResult(wire.ApplicationActivationOutcomeUnavailable, "")
	}
	descriptor, exists := d.descriptors[application]
	if !exists {
		d.mu.Unlock()
		return activationResult(wire.ApplicationActivationOutcomeUnknown, "")
	}
	if descriptor.Activation == nil {
		d.mu.Unlock()
		return activationResult(wire.ApplicationActivationOutcomeDisabled, "")
	}
	attempt := d.attempts[application]
	if attempt != nil && attempt.session != session {
		d.mu.Unlock()
		return activationResult(wire.ApplicationActivationOutcomeForbidden, "session")
	}
	if attempt != nil && attempt.generation != d.generations[application] {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return activationResult(wire.ApplicationActivationOutcomeUnavailable, "cancelled")
		case <-attempt.done:
			return attempt.result
		}
	}
	if instance := d.readyInstanceLocked(application, descriptor.Activation.Readiness, session); instance != "" {
		if attempt := d.attempts[application]; attempt != nil && attempt.completed {
			delete(d.attempts, application)
		}
		d.mu.Unlock()
		return wire.ApplicationActivationResult{Outcome: wire.ApplicationActivationOutcomeReady, Instance: instance}
	}
	attempt = d.attempts[application]
	if attempt == nil {
		if len(d.attempts) >= 64 {
			d.mu.Unlock()
			return activationResult(wire.ApplicationActivationOutcomeUnavailable, "capacity")
		}
		attempt = &applicationActivationAttempt{done: make(chan struct{}), generation: d.generations[application], session: session}
		d.attempts[application] = attempt
		go d.runActivation(application, descriptor, attempt)
	}
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return activationResult(wire.ApplicationActivationOutcomeUnavailable, "cancelled")
	case <-attempt.done:
		return attempt.result
	}
}
func (d *applicationDirectory) withdraw(ctx context.Context, s rights.Subject, name, id string) wire.ApplicationChange {
	return d.withdrawInSession(ctx, s, "test", name, id)
}

func (d *applicationDirectory) withdrawInSession(ctx context.Context, s rights.Subject, session, name, id string) wire.ApplicationChange {
	if !providerName.MatchString(name) || id == "" || len(id) > 128 {
		return appChange(wire.ApplicationOutcomeInvalid, "instance")
	}
	if session == "" {
		return appChange(wire.ApplicationOutcomeForbidden, "")
	}
	if out := d.authorized(ctx, s, ActionApplicationAnnounce, "app:"+name); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	defer d.mu.Unlock()
	d.expireLocked()
	l, ok := d.instances[id]
	if !ok {
		return appChange(wire.ApplicationOutcomeStale, "")
	}
	if l.application != name || l.owner.Account != s.Account || !samePrograms(l.owner.Program, s.Program) || l.session != session {
		return appChange(wire.ApplicationOutcomeForbidden, "")
	}
	delete(d.instances, id)
	d.notifyLocked()
	return appChange(wire.ApplicationOutcomeApplied, "")
}

func (d *applicationDirectory) snapshot(ctx context.Context, s rights.Subject) (wire.ApplicationPage, <-chan struct{}, time.Duration) {
	if !d.lock(ctx) {
		return wire.ApplicationPage{Outcome: wire.ApplicationOutcomeUnavailable, Applications: []wire.ApplicationEntry{}}, nil, time.Second
	}
	defer d.mu.Unlock()
	d.expireLocked()
	out := wire.ApplicationPage{Outcome: wire.ApplicationOutcomePage, Applications: []wire.ApplicationEntry{}}
	if ctx.Err() != nil {
		out.Outcome = wire.ApplicationOutcomeUnavailable
		return out, d.changed, time.Second
	}
	if s.Account != d.owner || s.Program == "" {
		out.Outcome = wire.ApplicationOutcomeForbidden
		return out, d.changed, time.Second
	}
	until := 30 * time.Second
	for name, v := range d.descriptors {
		permission := d.authorized(ctx, s, ActionApplicationRead, "app:"+name)
		if permission == wire.ApplicationOutcomeUnavailable {
			return wire.ApplicationPage{Outcome: permission, Applications: []wire.ApplicationEntry{}}, d.changed, time.Second
		}
		if permission != wire.ApplicationOutcomeApplied {
			continue
		}
		entry := wire.ApplicationEntry{Scope: wire.ScopeLocal, Descriptor: v, Instances: []wire.ApplicationInstance{}}
		for _, l := range d.instances {
			if l.application == name {
				copy := l.value
				copy.Interfaces = append([]wire.ApplicationInterface{}, copy.Interfaces...)
				copy.Contexts = append([]wire.ApplicationContext{}, copy.Contexts...)
				entry.Instances = append(entry.Instances, copy)
				remaining := l.deadline.Sub(d.now())
				if remaining < until {
					until = remaining
				}
			}
		}
		sort.Slice(entry.Instances, func(i, j int) bool { return entry.Instances[i].Instance < entry.Instances[j].Instance })
		out.Applications = append(out.Applications, entry)
	}
	if ctx.Err() != nil {
		return wire.ApplicationPage{Outcome: wire.ApplicationOutcomeUnavailable, Applications: []wire.ApplicationEntry{}}, d.changed, time.Second
	}
	sort.Slice(out.Applications, func(i, j int) bool { return out.Applications[i].Descriptor.Name < out.Applications[j].Descriptor.Name })
	raw, _ := json.Marshal(struct {
		Epoch, Account, Program string
		Entries                 []wire.ApplicationEntry
	}{d.epoch, s.Account, s.Program, out.Applications})
	if len(raw) > 900*1024 {
		return wire.ApplicationPage{Outcome: wire.ApplicationOutcomeUnavailable, Applications: []wire.ApplicationEntry{}}, d.changed, time.Second
	}
	sum := sha256.Sum256(raw)
	out.Cursor = d.epoch + "." + hex.EncodeToString(sum[:])
	return out, d.changed, until
}
func (d *applicationDirectory) observe(ctx context.Context, s rights.Subject, cursor string, waitMS int64) wire.ApplicationPage {
	if len(cursor) > 128 || waitMS < 0 || waitMS > 30000 {
		return wire.ApplicationPage{Outcome: wire.ApplicationOutcomeInvalid, Applications: []wire.ApplicationEntry{}}
	}
	end := time.Now().Add(time.Duration(waitMS) * time.Millisecond)
	for {
		page, changed, expiry := d.snapshot(ctx, s)
		if page.Outcome == wire.ApplicationOutcomePage && cursor != "" && !strings.HasPrefix(cursor, d.epoch+".") {
			page.Outcome = wire.ApplicationOutcomeStale
			return page
		}
		if page.Outcome != wire.ApplicationOutcomePage || page.Cursor != cursor || waitMS == 0 || !time.Now().Before(end) {
			return page
		}
		remaining := time.Until(end)
		if expiry < remaining {
			remaining = expiry
		}
		if remaining < time.Millisecond {
			remaining = time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return wire.ApplicationPage{Outcome: wire.ApplicationOutcomeUnavailable, Applications: []wire.ApplicationEntry{}}
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
