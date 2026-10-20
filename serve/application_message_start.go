package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

const applicationMessageStartsFile = "message-start-reservations.json"
const applicationMessageStartsSchemaFile = "message-start-schema.json"
const applicationMessageStartsLimit = 256 << 10

type applicationMessageStartSchema struct {
	Version int `json:"version"`
}

func validApplicationMessageStartSchema(raw []byte) bool {
	v := applicationMessageStartSchema{}
	return raw != nil && applicationV3Strict(raw, &v) == nil && v.Version == 1
}

// Reservations retain receiver/hour admission independently of registration.
// Messages durably claims each canonical attempt before calling this owner and
// keeps that effect custody after these hourly reservations are reclaimed.
type applicationMessageStartReservation struct {
	Account string `json:"account"`
	Program string `json:"program"`
	Attempt string `json:"attempt"`
	Message string `json:"message"`
	At      int64  `json:"at"`
}

// The runtime constructs this private request from the accepted question and
// approved profile. Validate owns live person consent and bounded profile proof.
type applicationMessageStartRequest struct {
	Application, Program, ProgramKey, Account, Session, Context string
	RegistrationBinding, EnrollmentRevision, Attempt, Message   string
	ProfileFingerprint, Kind                                    string
	// PermitRecipe and PermitRegistration name the recipe and registration a
	// start permit binds, without the recipe's saved command identity.
	PermitRecipe, PermitRegistration string
	// PerHour is captured from Messages' effective Config policy before Rights.
	// Zero retains the default for existing private callers.
	PerHour    int
	Descriptor wire.ApplicationDescriptor
	Validate   func(context.Context) error

	// Target is the queued question's attach context, empty for the recipe's
	// own Context. It routes the start; consent and reservation ignore it.
	Target string
	Cold   *startprofile.ColdResume
	// Origin is the chain of question IDs the woken turn carries: the waking
	// question's own chain and that question.
	Origin []string
	// Continue is checked while the woken turn runs. It refuses once the
	// person stopped the turn or withdrew the start permit; an answered
	// question does not end a turn that is finishing its reply.
	Continue func(context.Context) error
	// Delivered receives how an addressed session received the turn, as
	// the launcher reports it on the private owned-start channel.
	Delivered func(string)
}

func (d *applicationDirectory) prepareMessageStart(ctx context.Context, application string) (applicationMessageStartRequest, error) {
	if d.messageStartProfile == nil {
		return applicationMessageStartRequest{}, errors.New("message start: approved profile unavailable")
	}
	var image applicationAgentImage
	view := d.currentAgentEnrollmentImage(ctx, application, &image)
	if view.Outcome != applications.OutcomePage || view.Enrollment == nil || view.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
		return applicationMessageStartRequest{}, errors.New("message start: enrollment")
	}
	if !d.lock(ctx) {
		return applicationMessageStartRequest{}, ctx.Err()
	}
	var descriptor wire.ApplicationDescriptor
	var binding string
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		var ok bool
		descriptor, binding, ok = d.agentEntryLocked(legacy, application)
		if !ok {
			return errors.New("message start: registration")
		}
		return nil
	})
	d.unlock()
	if err != nil {
		return applicationMessageStartRequest{}, err
	}
	r, err := d.messageStartProfile(ctx, descriptor)
	if err != nil {
		return applicationMessageStartRequest{}, err
	}
	if r.Session == "" || r.Context == "" || r.ProfileFingerprint == "" || r.Validate == nil {
		return applicationMessageStartRequest{}, errors.New("message start: profile bounds")
	}
	r.Application, r.Program, r.ProgramKey, r.Account = application, image.Program, image.Key, d.owner
	r.RegistrationBinding, r.EnrollmentRevision, r.Descriptor = binding, view.Enrollment.Revision, descriptor
	r.PermitRegistration = messageStartPermitRegistration(descriptor)
	return r, nil
}

type applicationMessageStartResult struct{ State, Reason, Instance string }

func (d *applicationDirectory) messageStartBindingLocked(ctx context.Context, r applicationMessageStartRequest, legacy map[string]wire.ApplicationDescriptor) error {
	v, binding, ok := d.agentEntryLocked(legacy, r.Application)
	marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
	if err != nil {
		return err
	}
	m := marks[r.Application]
	left, _ := json.Marshal(v)
	right, _ := json.Marshal(r.Descriptor)
	if !ok || string(left) != string(right) || binding != r.RegistrationBinding || m.Removing || m.Kind != "agent" || m.Agent == nil || m.Agent.State != "enrolled" || m.Agent.Activation != nil || m.Agent.Account != r.Account || m.RegistrationRevision != r.EnrollmentRevision || m.ProgramKey != r.ProgramKey || !samePrograms(v.Program, r.Program) {
		return errors.New("message start: registration changed")
	}
	return nil
}

func (d *applicationDirectory) recheckMessageStart(ctx context.Context, r applicationMessageStartRequest) error {
	if r.Validate == nil {
		return errors.New("message start: live authority unavailable")
	}
	if err := r.Validate(ctx); err != nil {
		return err
	}
	var image applicationAgentImage
	v := d.currentAgentEnrollmentImage(ctx, r.Application, &image)
	if v.Outcome != applications.OutcomePage || v.Enrollment == nil || v.Enrollment.State != applications.AgentEnrollmentStateEnrolled || v.Enrollment.Revision != r.EnrollmentRevision || image.Key != r.ProgramKey || !samePrograms(image.Program, r.Program) {
		return errors.New("message start: image changed")
	}
	return nil
}

// recheckReservedMessageStart is the admitted launch/receiver authority guard.
// Its caller's canonical question and pair-receipt checks run before taking any
// directory lock. The application owner then verifies its durable admission and
// current registration independently. Native child matching belongs to the
// owned process owner consuming this guard.
func (d *applicationDirectory) recheckReservedMessageStart(ctx context.Context, r applicationMessageStartRequest) error {
	if !messageStartKind(r.Kind) || r.ProfileFingerprint == "" || r.Session == "" || r.Context == "" {
		return errors.New("message start: admitted question binding")
	}
	if err := d.recheckMessageStart(ctx, r); err != nil {
		return err
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	return d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if err := d.messageStartBindingLocked(ctx, r, legacy); err != nil {
			return err
		}
		marker, err := cas.ReadLimitContext(ctx, filepath.Join(d.dir, applicationMessageStartsSchemaFile), 64)
		if err != nil {
			return err
		}
		if !validApplicationMessageStartSchema(marker) {
			return errors.New("message start: schema unavailable")
		}
		raw, err := cas.ReadLimitContext(ctx, filepath.Join(d.dir, applicationMessageStartsFile), applicationMessageStartsLimit)
		if err != nil {
			return err
		}
		state, err := decodeApplicationMessageStarts(raw)
		if err != nil {
			return err
		}
		for _, reservation := range state.Reservations {
			if reservation.Account == r.Account && samePrograms(reservation.Program, r.Program) && reservation.Attempt == r.Attempt && reservation.Message == r.Message {
				return nil
			}
		}
		return errors.New("message start: durable admission unavailable")
	})
}

func (d *applicationDirectory) messageStartReadyLocked(r applicationMessageStartRequest) string {
	d.expireLocked()
	if d.nativeInboxes == nil || d.nativeInboxes.closed {
		return ""
	}
	row := d.nativeInboxes.rows[r.Application+"\x00"+r.inbox()]
	if row == nil || row.admission == nil || row.admission.Enrollment.Revision != r.EnrollmentRevision || row.admission.Image.Key != r.ProgramKey || row.session != r.Session || row.subject.Account != r.Account || !samePrograms(row.subject.Program, r.Program) {
		return ""
	}
	lease, ok := d.instances[row.instance]
	if !ok || lease.application != r.Application || lease.session != r.Session || !d.now().Before(lease.deadline) {
		return ""
	}
	_, subject, session, proc, err := nativeInboxPeer(row.pin, d.now())
	if err != nil || subject.Account != r.Account || !samePrograms(subject.Program, r.Program) || session != r.Session || !sameInboxProcess(proc, row.process) {
		return ""
	}
	return row.instance
}

func (d *applicationDirectory) startMessageReceiver(ctx context.Context, r applicationMessageStartRequest) (out applicationMessageStartResult) {
	refuse := func(reason string) applicationMessageStartResult {
		return applicationMessageStartResult{State: "refused", Reason: reason}
	}
	if d.launchMessageStart == nil || !messageStartKind(r.Kind) || r.Validate == nil || r.ProfileFingerprint == "" || r.Account != d.owner || r.Session == "" || r.Context == "" || r.ProgramKey == "" || r.Attempt == "" || r.Message == "" || r.Descriptor.Activation == nil || r.Descriptor.Activation.ReadinessTimeoutMs <= 0 || r.Descriptor.Activation.ReadinessTimeoutMs > 60000 {
		return refuse("binding")
	}
	if err := d.recheckMessageStart(ctx, r); err != nil {
		return refuse("authorization")
	}
	if !d.lock(ctx) {
		return refuse("cancelled")
	}
	// Every existing instance suppresses auto-start, including an instance whose
	// inbox is closed or whose context differs from the approved routing label.
	d.expireLocked()
	if d.attempts[r.Application] != nil {
		d.unlock()
		return refuse("starting")
	}
	if r.Target != "" && (!validMessageStartSelectedTarget(r) || d.messageContextOpenLocked(r.Application, r.Target)) {
		d.unlock()
		return refuse("running")
	}
	for _, lease := range d.instances {
		if r.Target == "" && lease.application == r.Application {
			d.unlock()
			return refuse("running")
		}
	}
	ok, err := d.reserveMessageStartLocked(ctx, applicationMessageStartReservation{Account: r.Account, Program: r.Program, Attempt: r.Attempt, Message: r.Message}, func(legacy map[string]wire.ApplicationDescriptor) error {
		return d.messageStartBindingLocked(ctx, r, legacy)
	}, r.PerHour)
	var attempt *applicationActivationAttempt
	if err == nil && ok {
		attempt = &applicationActivationAttempt{done: make(chan struct{}), generation: d.generations[r.Application], session: r.Session}
		d.attempts[r.Application] = attempt
	}
	d.unlock()
	if err != nil || !ok {
		return refuse("reservation")
	}
	issued := false
	defer func() {
		result := activationResult(wire.ApplicationActivationOutcomeUnknown, out.Reason)
		result.Started = issued
		if out.State == "ready" {
			result.Outcome, result.Instance = wire.ApplicationActivationOutcomeReady, out.Instance
		}
		d.finishActivation(r.Application, attempt, result, issued && out.State != "ready")
	}()
	// A committed reservation conservatively consumes admission on every failure.
	// Messages keeps canonical uncertain-attempt custody beyond this hourly quota.
	if err := d.recheckMessageStart(ctx, r); err != nil {
		return refuse("authorization_changed")
	}
	if !d.lock(ctx) {
		return refuse("cancelled")
	}
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if d.attempts[r.Application] != attempt || attempt.completed || d.generations[r.Application] != attempt.generation {
			return errors.New("message start: generation changed")
		}
		if err := d.messageStartBindingLocked(ctx, r, legacy); err != nil {
			return err
		}
		if r.Target != "" && d.messageContextOpenLocked(r.Application, r.Target) {
			return errors.New("message start: running")
		}
		for _, lease := range d.instances {
			if r.Target == "" && lease.application == r.Application {
				return errors.New("message start: running")
			}
		}
		return nil
	})
	d.unlock()
	if err != nil {
		return refuse("registration_changed")
	}
	// The owned launcher receives the admitted immutable snapshot and live guard
	// after both locks are released. It proves the actual child occurrence.
	result := make(chan error, 1)
	var dispatched <-chan error = result
	authority := func(ctx context.Context) error { return d.recheckReservedMessageStart(ctx, r) }
	if err := authority(ctx); err != nil {
		return refuse("authority_changed")
	}
	issued = true
	go func() {
		// While the turn runs, the messages it sends carry its origin chain.
		defer d.messageWakes.begin(r.Application, r.inbox(), r.Attempt, r.Origin)()
		result <- d.launchMessageStart(ctx, r, authority)
	}()
	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(r.Descriptor.Activation.ReadinessTimeoutMs)*time.Millisecond)
	defer cancel()
	for {
		if !d.lock(readyCtx) {
			return applicationMessageStartResult{State: "unknown", Reason: "readiness"}
		}
		instance := d.messageStartReadyLocked(r)
		changed := d.changed
		d.unlock()
		if instance != "" {
			return applicationMessageStartResult{State: "ready", Instance: instance}
		}
		select {
		case launchErr := <-dispatched:
			dispatched = nil
			if launchErr != nil {
				return applicationMessageStartResult{State: "unknown", Reason: "launch"}
			}
		case <-changed:
		case <-readyCtx.Done():
			return applicationMessageStartResult{State: "unknown", Reason: "readiness"}
		}
	}
}

type applicationMessageStartState struct {
	Version      int                                  `json:"version"`
	Reservations []applicationMessageStartReservation `json:"reservations"`
}

func decodeApplicationMessageStarts(raw []byte) (applicationMessageStartState, error) {
	v := applicationMessageStartState{}
	if raw == nil || applicationV3Strict(raw, &v) != nil || v.Version != 1 || v.Reservations == nil || len(v.Reservations) > 512 {
		return v, errors.New("message start: invalid reservations")
	}
	seen := map[string]bool{}
	for _, r := range v.Reservations {
		if r.Account == "" || len(r.Account) > 4096 || !identity.ValidSubjectProgram(r.Program) || r.Attempt == "" || len(r.Attempt) > 256 || r.Message == "" || len(r.Message) > 256 || r.At <= 0 || seen[r.Attempt] {
			return v, errors.New("message start: invalid reservation")
		}
		seen[r.Attempt] = true
	}
	return v, nil
}

// initializeMessageStarts runs only while opening the application owner. Every
// subsequent admission requires the existing file and its strict encoding.
func (d *applicationDirectory) initializeMessageStarts() error {
	ctx := context.Background()
	// Open runs before the @3 projection is attached; take the same descriptor
	// CAS directly without requiring its later manifest decoder composition.
	return cas.ChangeLimitContext(ctx, filepath.Join(d.dir, "descriptors.json"), d.store.MaxBytes, func(descriptors []byte) ([]byte, error) {
		if _, err := decodeLegacyApplications(descriptors); err != nil {
			return nil, err
		}
		marker, err := cas.ReadLimitContext(ctx, filepath.Join(d.dir, applicationMessageStartsSchemaFile), 64)
		if err != nil {
			return nil, err
		}
		if marker != nil && !validApplicationMessageStartSchema(marker) {
			return nil, errors.New("message start: invalid schema")
		}
		err = cas.ChangeLimitContext(ctx, filepath.Join(d.dir, applicationMessageStartsFile), applicationMessageStartsLimit, func(raw []byte) ([]byte, error) {
			if raw == nil && marker == nil {
				return json.Marshal(applicationMessageStartState{Version: 1, Reservations: []applicationMessageStartReservation{}})
			}
			_, err := decodeApplicationMessageStarts(raw)
			return raw, err
		})
		if err != nil {
			return nil, err
		}
		// Data becomes durable first. Interrupted initialization keeps any data
		// and completes only this schema marker on the next owner open.
		err = cas.ChangeLimitContext(ctx, filepath.Join(d.dir, applicationMessageStartsSchemaFile), 64, func(raw []byte) ([]byte, error) {
			if raw != nil && !validApplicationMessageStartSchema(raw) {
				return nil, errors.New("message start: invalid schema")
			}
			return json.Marshal(applicationMessageStartSchema{Version: 1})
		})
		return descriptors, err
	})
}

// reserveMessageStartLocked is called with d.mu held after message eligibility,
// person consent and pair-budget admission. It commits before process dispatch.
func (d *applicationDirectory) reserveMessageStartLocked(ctx context.Context, r applicationMessageStartReservation, check func(map[string]wire.ApplicationDescriptor) error, limits ...int) (bool, error) {
	limit := 1
	if len(limits) > 1 {
		return false, errors.New("message start: invalid rate")
	}
	if len(limits) == 1 && limits[0] != 0 {
		limit = limits[0]
	}
	if !validMessageStartRate(limit) {
		return false, errors.New("message start: invalid rate")
	}
	admitted := false
	if check == nil || r.Account != d.owner || !identity.ValidSubjectProgram(r.Program) || r.Attempt == "" || r.Message == "" {
		return false, errors.New("message start: binding")
	}
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marker, err := cas.ReadLimitContext(ctx, filepath.Join(d.dir, applicationMessageStartsSchemaFile), 64)
		if err != nil {
			return err
		}
		if !validApplicationMessageStartSchema(marker) {
			return errors.New("message start: schema unavailable")
		}
		if err := check(legacy); err != nil {
			return err
		}
		return cas.ChangeLimitContext(ctx, filepath.Join(d.dir, applicationMessageStartsFile), applicationMessageStartsLimit, func(raw []byte) ([]byte, error) {
			v, err := decodeApplicationMessageStarts(raw)
			if err != nil {
				return nil, err
			}
			now := d.now().UnixMilli()
			retained := make([]applicationMessageStartReservation, 0, len(v.Reservations))
			count := 0
			for _, prior := range v.Reservations {
				// A backward clock keeps every potentially current reservation.
				if now >= prior.At && now-prior.At >= time.Hour.Milliseconds() {
					continue
				}
				retained = append(retained, prior)
				if prior.Attempt == r.Attempt || prior.Message == r.Message {
					return nil, errors.New("message start: reserved")
				}
				if prior.Account == r.Account && samePrograms(prior.Program, r.Program) && (now < prior.At || now-prior.At < time.Hour.Milliseconds()) {
					count++
				}
			}
			if count >= limit {
				return nil, errors.New("message start: receiver hour")
			}
			v.Reservations = retained
			if len(v.Reservations) >= 512 {
				return nil, errors.New("message start: capacity")
			}
			r.At = now
			v.Reservations = append(v.Reservations, r)
			encoded, err := json.Marshal(v)
			if err == nil {
				_, err = decodeApplicationMessageStarts(encoded)
			}
			admitted = err == nil
			return encoded, err
		})
	})
	return admitted && err == nil, err
}

// messageStartPermitRegistration names the receiver's registration for its
// start permit without the activation recipe, which the permit names through
// its PermitRecipe: a re-pinned saved command changes neither.
func messageStartPermitRegistration(descriptor wire.ApplicationDescriptor) string {
	descriptor.Activation = nil
	raw, _ := json.Marshal(descriptor)
	return applicationV3Hash(raw)
}
