package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	rawcas "github.com/openabstractions/abstraction-cas/go"
	cas "github.com/openabstractions/abstraction-cas/go/api"
	watch "github.com/openabstractions/abstraction-watch/go"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

const (
	ActionApplicationManage   = "abstraction.facade/application.manage"
	ActionApplicationRead     = "abstraction.facade/application.read"
	ActionApplicationAnnounce = "abstraction.facade/application.announce"
	ActionApplicationActivate = "abstraction.facade/application.activate"
	ActionApplicationMessage  = "abstraction.facade/application.message"
	// ActionApplicationRegisterSelf is Register of the caller's own program,
	// decided on account (rights.ActionApplicationRegisterSelf).
	ActionApplicationRegisterSelf = "abstraction.facade/application.register_self"
	ApplicationsContract          = "abstraction.facade/applications@2"
)

type applicationLease struct {
	application    string
	owner          rights.Subject
	session        string
	value          wire.ApplicationInstance
	deadline       time.Time
	agentAdmission *applicationAgentAdmission
}

type applicationActivationAttempt struct {
	done       chan struct{}
	result     wire.ApplicationActivationResult
	completed  bool
	generation uint64
	session    string
}

type applicationDirectory struct {
	messageRecheck   *watch.Recheck // guarded by mu
	mu               sync.RWMutex
	dir, epoch       string
	base             cas.Value
	store            cas.BoundedFileStore
	owner            string
	descriptors      map[string]wire.ApplicationDescriptor
	v3               *applicationV3Registrations
	programsChanged  func()
	programKeepImage func(context.Context, applicationV3Source) (activationImageHold, error)
	// The config catalogue owns key meaning; nil admits no declared config key.
	programConfigKeyKnown  func(string) bool
	programConfigKeep      func(context.Context, string, []string) error
	programConfigRemove    func(context.Context, string) error
	programConfigReconcile func(context.Context, map[appliedRule]bool) error
	programConfigReport    func(error)
	// programHoldKeep, when set, writes the hold rules a kept integration's
	// program gets (registry_v3_keep_hold.go).
	programHoldKeep func(program string) error
	// programHoldRetire, when set, removes the hold rules written for
	// programs that are no longer a kept integration.
	programHoldRetire    func(ctx context.Context, kept []string) error
	programConfigManaged func(context.Context, *identity.Peer, providerConfigRegistration) error
	// roleDecode is the native manifest owner's pure declaration projection.
	roleDecode func([]byte, string) (bool, error)
	roles      atomic.Pointer[applicationRoles]
	// agentClass is the last messaging facts prepared or published. The
	// agent-messages default reads it while a registration write has
	// cleared roles; only the next successful preparation replaces it.
	agentClass           atomic.Pointer[messagePartyFacts]
	messageRegistrations [4]*messageRegistrationSnapshot // guarded by mu
	// v3Changed closes mediation ownership for an obsolete revision, outside
	// storage callbacks and the directory lock. It never owns GUI/job lifetime.
	v3Changed func(name, revision string)
	// unconfirmed holds the entries their own program registered under the
	// registration default and the person has not kept, each with the
	// question asked for that registration and the publisher bound from it,
	// kept in self-registered.json beside descriptors.json; selfBase is that
	// file's last read or written value.
	unconfirmed map[string]selfMark
	selfBase    cas.Value
	// pins holds each applications@2 entry's continuity owner, its signer or
	// unsigned image pin, kept in application-pins.json; pinsBase is that
	// file's last read or written value (application_legacy_pins.go).
	pins     map[string]applicationLegacyPin
	pinsBase cas.Value
	// legacyPin, when set, replaces applicationLegacyImagePin, the read of a
	// registered program's signer or image pin.
	legacyPin   func(context.Context, string) (applicationLegacyPin, error)
	instances   map[string]applicationLease
	attempts    map[string]*applicationActivationAttempt
	generations map[string]uint64
	changed     chan struct{}
	gate        *watch.Gate
	now         func() time.Time
	decide      func(context.Context, rights.Subject, string, string) (bool, error)
	// registeredSelf, when set, runs after a program registered its own
	// descriptor, outside the directory lock: the runtime lets its operator
	// programs read and start the entry.
	registeredSelf func(name string)
	// askKeep, when set, has the runtime ask the person whether to keep the
	// self-registered entry name (runtime_self_registered.go).
	askKeep func(name string) error
	// connects holds the Panel's pending Connect clicks
	// (application_connect_expectation.go); connectPanel admits the Panel
	// program that records them, and connectPeer requires its live transport
	// identity before recording a click. keepConnected, when set, keeps a waiting
	// registration a Connect click matched; connectReport reports a Keep
	// record that could not be written.
	connects      connectExpectations
	connectPanel  func(program string) bool
	connectPeer   func(context.Context, *identity.Peer) bool
	keepConnected func(name, revision string) bool
	connectReport func(error)
	// catalogueHostHold reopens the installed host held by an agent Keep.
	// Tests substitute only the platform image proof.
	catalogueHostHold func(context.Context, catalogueHostRecord) (*heldApplicationManifest, error)
	// reviewUnsigned, when set, has the runtime ask the person about an
	// unsigned program whose image no longer matches the one they kept
	// (application_v3_unsigned.go). It returns at once.
	reviewUnsigned func(name, revision string)
	// unsignedHeldBack, when set, reports that name's image at revision, read
	// by its cheap file stamp, is the changed image already under review or
	// answered Not now. Revalidation then refuses it without hashing it.
	unsignedHeldBack func(name, revision, stamp string) bool
	// unsignedLaunch, when set, records name starting, so a changed image the
	// person answered Not now about is asked about again. person is a start
	// the person made through OpenAbstractions; a program announcing its own
	// start re-asks at most once an hour per image.
	unsignedLaunch func(name string, person bool)
	// imagePin, when set, replaces applicationHeldPin, the full read of a
	// held image's SHA-256 and size.
	imagePin func(context.Context, *heldApplicationManifest) (string, int64, error)
	launch   func(string, []string) error
	stat     func(string) (os.FileInfo, error)
	// starts is the start log, oldest first, startsSince its eviction time
	// and startsBase starts.json's last read or written value
	// (application_starts.go); operator admits ListStarts' callers.
	starts         []wire.ApplicationStart
	startsSince    int64
	startsBase     cas.Value
	operator       func(program string) bool
	agentQuestions *agentEnrollmentQuestions
	agentImage     func(context.Context, string, string) (applicationAgentImage, error)
	agentExcluded  func(context.Context, string, string) (bool, error)
	// agentMaintenance updates a kept connection under its first Keep
	// (ASK-R12): a same-source update that widens nothing commits with no
	// question, posts one notice with Undo and adds its Activity line
	// (architect popup ruling 2026-11-10, row 8). It is on for Windows; on
	// macOS the person's Update stays the consent in 0.8.0 (owner,
	// 2026-11-04). Tests set it independently.
	agentMaintenance bool
	// agentInstalledTrusted refuses an installed tools copy that a principal
	// other than SYSTEM, Administrators or TrustedInstaller could change;
	// nil uses trustedpath.CheckFile. Tests set it.
	agentInstalledTrusted  func(program string) error
	agentPublishedSource   func(context.Context) (agentUpdateImage, error)
	agentCompanionProducer agentCompanionProducer
	// agentStamp reads an image's cheap stamp; nil uses the platform owner.
	// agentImages retains verifications admission reuses while it is unchanged.
	agentStamp  func(string) (applicationAgentStamp, error)
	agentImages applicationAgentImages
	// Set only by the batch owner while holding mu and the registration CAS.
	agentUpdateEdits   map[string]bool
	agentUpdateRetired []applicationAgentUpdate
	// messageBlock retires old-target questions and invalidates only the private
	// message permission while a durable registration reservation is held.
	messageBlock        func(context.Context, string) error
	nativeInboxes       *nativeInboxBook
	nativeInboxesClosed bool
	// Only trusted runtime composition supplies bounded native profile receiving.
	messageStartProfile func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error)
	// messageStartChatsOnly limits starts to existing retained chats: a
	// question never starts a new session, the recipe's own included.
	messageStartChatsOnly bool
	// messageCodexRunning reports whether the person's Codex app runs. A
	// retained Codex chat is woken only through that app's queue; nil or an
	// error counts as closed.
	messageCodexRunning   func(context.Context) (bool, error)
	activationConfigFault func(string) error // private source-fixture interruption seam
	launchMessageStart    func(context.Context, applicationMessageStartRequest, func(context.Context) error) error
	messageStartOwned     *ownedMessageStarts
	// messageWakes holds each running woken turn's origin chain by its
	// application and inbox context, for the messages it sends.
	messageWakes messageWakes

	// presenceEnds keeps each receiver context's most recent presence end,
	// so a permitted sender learns why no inbox is live. presenceLapsed
	// records a lapse under the receiver's subject, outside the lock.
	presenceEnds   map[string]applicationPresenceEnd
	presenceLapsed func(program, application, contextName string, at time.Time)

	// messageAttachSessions lists an agent's existing sessions as attach
	// contexts. Nil keeps attach closed, like the production start resolver.
	messageAttachSessions func(context.Context, string) ([]applicationAttachSession, error)
	// messageAttachRefresh refreshes that listing once, for a Send to a
	// conversation missing from it.
	messageAttachRefresh func(context.Context, string) []applicationAttachSession
	// messageAttachWarm starts every named agent's refresh and waits once.
	messageAttachWarm func(context.Context, []string) bool
	// messageAttachCurrent reads an agent's snapshot without waiting.
	messageAttachCurrent func(string) []applicationAttachSession
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
		descriptors: map[string]wire.ApplicationDescriptor{}, instances: map[string]applicationLease{}, attempts: map[string]*applicationActivationAttempt{}, generations: map[string]uint64{}, changed: make(chan struct{}), gate: watch.NewGate(watch.DefaultWaiters), now: time.Now, decide: decide, launch: launchApplication, stat: os.Stat,
		agentMaintenance: runtime.GOOS == "windows"}
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
	if err := d.loadLegacyPins(); err != nil {
		return nil, err
	}
	d.unconfirmed = map[string]selfMark{}
	d.selfBase, err = d.store.Read(filepath.Join(d.dir, selfRegisteredFile))
	if err != nil {
		return nil, err
	}
	if d.selfBase.Data != nil {
		if decodeApplicationSelfMarks(d.selfBase.Data, &d.unconfirmed) != nil || d.unconfirmed == nil || len(d.unconfirmed) > 64 {
			return nil, errors.New("applications: invalid self-registered entries")
		}
		for name, mark := range d.unconfirmed {
			if !providerName.MatchString(name) || !mark.valid() || mark.Kind == "agent" && mark.Agent.Account != d.owner {
				return nil, errors.New("applications: invalid self-registered entry")
			}
		}
	}
	// Existing recorded questions retain their occurrence IDs. A private
	// registration revision prevents a new registration from sharing them.
	migrated := maps.Clone(d.unconfirmed)
	needsMigration := false
	for name, mark := range migrated {
		if _, active := d.descriptors[name]; active && mark.genericPending() && mark.RegistrationRevision == "" {
			revision, e := applicationToken()
			if e != nil {
				return nil, e
			}
			mark.RegistrationRevision = revision
			mark.ProgramKey = identity.ProgramKey(mark.Program)
			mark.UnboundQuestion = mark.Question
			mark.Question = ""
			migrated[name] = mark
			needsMigration = true
		}
	}
	if needsMigration {
		if err := d.persistUnconfirmedLocked(context.Background(), migrated); err != nil {
			return nil, err
		}
	}
	d.loadStarts()
	if err := d.initializeMessageStarts(); err != nil {
		return nil, err
	}
	return d, nil
}

// connectorApplicationName reports a name of the agent connectors' form,
// "oa-<agent>-integration" with any suffix. Setup registers its connectors
// through application.manage; a program registering itself may not take
// such a name, which the Panel would read as the agent it connects.
func connectorApplicationName(name string) bool {
	rest, ok := strings.CutPrefix(name, "oa-")
	return ok && strings.Contains(rest, "-integration")
}

// selfRegisteredFile holds the unconfirmed self-registered entries.
const selfRegisteredFile = "self-registered.json"

// selfMark is one unconfirmed self-registered entry: the id of the question
// the runtime admitted for this registration, empty until one is admitted,
// and the program and publisher bound from the registering connection, which
// every question about this registration shows. Program is the path the
// platform proved for the caller, which may be spelled otherwise than the
// descriptor's program (a junction, a short name).
type selfMark struct {
	Kind                 string                `json:"kind,omitempty"`
	Agent                *applicationAgentMark `json:"agent,omitempty"`
	V3Revision           string                `json:"-"`
	RegistrationRevision string                `json:"registration_revision,omitempty"`
	// An older writer supplied no generation binding. Its occurrence remains
	// historical; only a fresh existing Keep/Remove question may act now.
	UnboundQuestion string `json:"unbound_question,omitempty"`
	Removing        bool   `json:"removing,omitempty"`
	RemovalRevision string `json:"removal_revision,omitempty"`
	Question        string `json:"question,omitempty"`
	Program         string `json:"program,omitempty"`
	ProgramKey      string `json:"program_key,omitempty"`
	Publisher       string `json:"publisher,omitempty"`
	Trusted         bool   `json:"trusted,omitempty"`
	Description     string `json:"description,omitempty"`
	OriginalName    string `json:"original_name,omitempty"`
}

func (m selfMark) registrationRevision() string {
	if m.V3Revision != "" {
		return m.V3Revision
	}
	return m.RegistrationRevision
}

func newSelfMark(program string, p asks.Publisher) selfMark {
	return selfMark{Program: program, Publisher: p.Subject, Trusted: p.Trusted, Description: p.Description, OriginalName: p.OriginalName}
}

func (m selfMark) publisher() asks.Publisher {
	return asks.Publisher{Subject: m.Publisher, Trusted: m.Trusted, Description: m.Description, OriginalName: m.OriginalName}
}

// boundProgram is the program the question names: the caller's bound
// program, or the descriptor's for a mark written without one.
func (m selfMark) boundProgram(v wire.ApplicationDescriptor) string {
	if m.Program != "" {
		return m.Program
	}
	return v.Program
}

func (m selfMark) valid() bool {
	if m.Kind != "" || m.Agent != nil {
		return m.Kind == "agent" && m.Agent != nil && m.validAgent()
	}
	if m.RegistrationRevision != "" {
		if len(m.RegistrationRevision) != 48 {
			return false
		}
		if _, err := hex.DecodeString(m.RegistrationRevision); err != nil {
			return false
		}
	}
	if m.Removing {
		if m.Question == "" || m.RegistrationRevision == "" || len(m.RemovalRevision) != 64 {
			return false
		}
		if _, err := hex.DecodeString(m.RemovalRevision); err != nil {
			return false
		}
	} else if m.RemovalRevision != "" {
		return false
	}
	for _, s := range []string{m.Question, m.UnboundQuestion, m.Publisher, m.Description, m.OriginalName} {
		if len(s) > 1024 || !utf8.ValidString(s) {
			return false
		}
	}
	return len(m.Program) <= 4096 && utf8.ValidString(m.Program) && len(m.ProgramKey) <= 1024 && utf8.ValidString(m.ProgramKey)
}

// persistUnconfirmedLocked writes next as the unconfirmed entries.
func (d *applicationDirectory) persistUnconfirmedLocked(ctx context.Context, next map[string]selfMark) error {
	return rawcas.ChangeLimitContext(ctx, filepath.Join(d.dir, "descriptors.json"), d.store.MaxBytes, func(current []byte) ([]byte, error) {
		legacy, err := decodeLegacyApplications(current)
		if err != nil {
			return nil, err
		}
		if err := d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy); err != nil {
			return nil, err
		}
		return current, nil
	})
}

func (d *applicationDirectory) persistUnconfirmedAtDescriptorLocked(ctx context.Context, next map[string]selfMark, legacy map[string]wire.ApplicationDescriptor) error {
	var retired []applicationAgentUpdate
	d.roles.Store(nil)
	if next == nil || len(next) > 64 {
		return errors.New("applications: invalid self registrations")
	}
	for _, m := range next {
		if !m.valid() || m.Kind == "agent" && m.Agent.Account != d.owner {
			return errors.New("applications: invalid self registration")
		}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = rawcas.ChangeLimitContext(ctx, filepath.Join(d.dir, selfRegisteredFile), d.store.MaxBytes, func(current []byte) ([]byte, error) {
		if (current == nil) != (d.selfBase.Data == nil) || !bytes.Equal(current, d.selfBase.Data) {
			return nil, rawcas.ErrMoved
		}
		marks := map[string]selfMark{}
		if current != nil && decodeApplicationSelfMarks(current, &marks) != nil || marks == nil {
			return nil, errors.New("applications: invalid self registrations")
		}
		if err := d.guardAgentUpdateMarks(marks, next); err != nil {
			return nil, err
		}
		remaining := agentUpdateIndex(next)
		for id, u := range agentUpdateIndex(marks) {
			if !u.Committed && u.Question != "" && remaining[id] == nil {
				retired = append(retired, *u)
			}
		}
		// Reservation retirement above belongs to this exact metadata mutation.
		raw, err = json.Marshal(next)
		if err != nil {
			return nil, err
		}
		for name, mark := range marks {
			if mark.Removing {
				if _, active := legacy[name]; active {
					if replacement, ok := next[name]; !ok || replacement != mark {
						return nil, errors.New("applications: Remove intent reserved")
					}
				}
			}
		}
		return raw, nil
	}); err != nil {
		return err
	}
	d.selfBase = cas.Value{Data: raw}
	d.unconfirmed = next
	d.agentUpdateRetired = append(d.agentUpdateRetired, retired...)
	return nil
}

// setMarkLocked records name as unconfirmed with mark.
func (d *applicationDirectory) setMarkLocked(ctx context.Context, name string, mark selfMark) error {
	if mark.RegistrationRevision == "" {
		revision, err := applicationToken()
		if err != nil {
			return err
		}
		mark.RegistrationRevision = revision
	}
	if old, ok := d.unconfirmed[name]; ok && old == mark {
		return nil
	}
	next := maps.Clone(d.unconfirmed)
	next[name] = mark
	return d.persistUnconfirmedLocked(ctx, next)
}

// clearMarkLocked records name as confirmed, writing only on a change.
func (d *applicationDirectory) clearMarkLocked(ctx context.Context, name string) error {
	if _, ok := d.unconfirmed[name]; !ok {
		return nil
	}
	next := maps.Clone(d.unconfirmed)
	delete(next, name)
	return d.persistUnconfirmedLocked(ctx, next)
}

// unconfirmedOfLocked names the unconfirmed entries the program registered.
func (d *applicationDirectory) unconfirmedOfLocked(program string) []string {
	var names []string
	for name := range d.unconfirmed {
		if !d.unconfirmed[name].pendingOwnership() {
			continue
		}
		if v, ok := d.descriptors[name]; ok && samePrograms(v.Program, program) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func applicationEncodedFits(v any, limit int) bool {
	raw, err := json.Marshal(v)
	return err == nil && len(raw) <= limit
}

// validApplicationDescriptor is the Facade's one manifest check, which the
// Panel also applies before it shows a manifest for confirmation.
func validApplicationDescriptor(v wire.ApplicationDescriptor) bool {
	return client.ValidApplicationDescriptor(v) == nil
}

func validApplicationInterface(v wire.ApplicationInterface) bool {
	return providerName.MatchString(v.Name) && len(v.Protocol) > 0 && len(v.Protocol) <= 128 && len(v.Contract) <= 256
}

func (d *applicationDirectory) authorized(ctx context.Context, s rights.Subject, action, resource string) wire.ApplicationOutcome {
	if ctx.Err() != nil {
		return wire.ApplicationOutcomeUnavailable
	}
	if s.Account != d.owner || s.Program == "" || movedConnectorCaller(s.Program) {
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
// A contended caller waits in the mutex queue through one helper goroutine,
// which hands the lock over or, after the caller left, releases it at once.
func (d *applicationDirectory) lock(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if !d.mu.TryLock() {
		acquired, left := make(chan struct{}), make(chan struct{})
		go func() {
			d.mu.Lock()
			select {
			case acquired <- struct{}{}:
			case <-left:
				d.mu.Unlock()
			}
		}()
		select {
		case <-acquired:
		case <-ctx.Done():
			close(left)
			return false
		}
	}
	if ctx.Err() != nil {
		d.mu.Unlock()
		return false
	}
	return true
}

// lockRead is for immutable directory admission. Writer cleanup remains
// with unlock; a reader never drains registrations or question retirements.
func (d *applicationDirectory) lockRead(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if !d.mu.TryRLock() {
		acquired, left := make(chan struct{}), make(chan struct{})
		go func() {
			d.mu.RLock()
			select {
			case acquired <- struct{}{}:
			case <-left:
				d.mu.RUnlock()
			}
		}()
		select {
		case <-acquired:
		case <-ctx.Done():
			close(left)
			return false
		}
	}
	if ctx.Err() != nil {
		d.mu.RUnlock()
		return false
	}
	return true
}

// unlock owns successful-CAS question cleanup for every directory caller.
// Registration/storage callbacks have returned before their caller unlocks.
// Capture under the directory lock, then call Book after every mutation lock
// has released; retirement never reacquires this directory lock.
func (d *applicationDirectory) unlock() {
	retired := d.agentUpdateRetired
	d.agentUpdateRetired = nil
	k := d.agentQuestions
	d.mu.Unlock()
	drainRetiredAgentUpdateQuestions(k, retired)
}
func appChange(outcome wire.ApplicationOutcome, reason string) wire.ApplicationChange {
	return wire.ApplicationChange{Outcome: outcome, Reason: reason}
}
func (d *applicationDirectory) notifyLocked() {
	d.notifyDirectoryLocked()
	d.recheckMessagesLocked()
}

func (d *applicationDirectory) notifyDirectoryLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
	d.gate.Advance()
}

func (d *applicationDirectory) recheckMessagesLocked() {
	if d.messageRecheck != nil {
		d.messageRecheck.Request()
	}
}

// loginState reads the account's confirmed registrations and their change
// signal together. Pending self-registrations, presence and provider Registry
// declarations do not establish the person's choice to keep an application.
func (d *applicationDirectory) loginState(ctx context.Context) (applicationLoginState, error) {
	if !d.lock(ctx) {
		return applicationLoginState{}, ctx.Err()
	}
	defer d.unlock()
	state := applicationLoginState{changed: d.changed}
	for name := range d.descriptors {
		if d.v3 != nil {
			if e, ok := d.v3.entries[name]; ok {
				if e.State != "pending" && e.State != "removing" {
					state.enabled = true
					break
				}
				continue
			}
		}
		if mark, pending := d.unconfirmed[name]; !pending || !mark.genericPending() {
			state.enabled = true
			break
		}
	}
	return state, nil
}

// register adds a descriptor. A holder of application.manage registers another
// program's descriptor; a program registers its own descriptor under
// application.register_self, which the account-wide registration default
// permits unless the program's own rule or the switch refuses it
// (APPLICATIONS.md, rights RIGHTS-R22).
func (d *applicationDirectory) register(ctx context.Context, s rights.Subject, v wire.ApplicationDescriptor) wire.ApplicationChange {
	return d.registerFrom(ctx, s, asks.Publisher{}, v)
}

// registerFrom is register with the publisher bound from the registering
// connection, which the question about a self-registration shows.
func (d *applicationDirectory) registerFrom(ctx context.Context, s rights.Subject, publisher asks.Publisher, v wire.ApplicationDescriptor) wire.ApplicationChange {
	own := validApplicationDescriptor(v) && samePrograms(s.Program, identity.NormalizeSubjectProgram(v.Program))
	action := ActionApplicationManage
	if own {
		action = ActionApplicationRegisterSelf
	}
	if out := d.authorized(ctx, s, action, "account"); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if !validApplicationDescriptor(v) {
		return appChange(wire.ApplicationOutcomeInvalid, "descriptor")
	}
	v.Program = identity.NormalizeSubjectProgram(v.Program)
	if !validApplicationDescriptor(v) || !own && samePrograms(s.Program, v.Program) {
		return appChange(wire.ApplicationOutcomeInvalid, "descriptor")
	}
	// A title that reads as OpenAbstractions is refused on both paths; an
	// entry stored before this check keeps loading (client.ReservedApplicationTitle).
	if client.ReservedApplicationTitle(v.Title) {
		return appChange(wire.ApplicationOutcomeInvalid, "title")
	}
	if own && connectorApplicationName(v.Name) {
		return appChange(wire.ApplicationOutcomeInvalid, "name")
	}
	var mark *selfMark
	if own {
		m := newSelfMark(s.Program, publisher)
		m.ProgramKey = identity.ProgramKey(s.Program)
		mark = &m
	}
	// The program's signer or image pin is read once, before the lock.
	change := d.addDescriptorPinned(ctx, v, mark, d.observeLegacyPin(ctx, v.Program))
	if own && change.Outcome == wire.ApplicationOutcomeApplied {
		// Registration asks nothing: the entry stays unconfirmed until the
		// person acts on it in Apps (AskConfirmation).
		if d.registeredSelf != nil {
			d.registeredSelf(v.Name)
		}
	}
	return change
}

// addDescriptor stores a checked descriptor under the directory lock, marked
// unconfirmed with mark when its own program registered it (mark not nil). A
// program has at most one unconfirmed entry: a second self-registration
// while one awaits the person is a conflict, reason "unconfirmed".
func (d *applicationDirectory) addDescriptor(ctx context.Context, v wire.ApplicationDescriptor, mark *selfMark) wire.ApplicationChange {
	return d.addDescriptorPinned(ctx, v, mark, nil)
}

// addDescriptorPinned is addDescriptor with the incoming program's pin, nil
// when none could be read: a new entry records it, and an update must carry
// the entry's recorded one (updateSameProgramLocked).
func (d *applicationDirectory) addDescriptorPinned(ctx context.Context, v wire.ApplicationDescriptor, mark *selfMark, pin *applicationLegacyPin) wire.ApplicationChange {
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	defer d.unlock()
	if d.messageBlock != nil {
		return d.publishMessageLegacyLocked(ctx, v, mark, pin)
	}
	if existing, ok := d.descriptors[v.Name]; ok {
		return d.updateSameProgramLocked(ctx, existing, v, pin)
	}
	if mark != nil && len(d.unconfirmedOfLocked(v.Program)) > 0 {
		return appChange(wire.ApplicationOutcomeConflict, "unconfirmed")
	}
	if len(d.descriptors) >= 64 {
		return appChange(wire.ApplicationOutcomeUnavailable, "capacity")
	}
	// The pin and the mark are written before the descriptor: a left-over
	// pin or mark for a name with no descriptor binds nothing, and the next
	// registration rewrites it.
	if err := d.setLegacyPinLocked(ctx, v.Name, pin); err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
	}
	var err error
	if mark != nil {
		err = d.setMarkLocked(ctx, v.Name, *mark)
	} else {
		err = d.clearMarkLocked(ctx, v.Name)
	}
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
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

// updateSameProgramLocked answers a registration under a name already taken.
// Register is create-only: the same program registering again updates its
// descriptor and keeps the entry's confirmation and marks, when it carries
// the entry's recorded signer or image pin (application_legacy_pins.go). Any
// other program, and the same path holding another identity, is refused with
// conflict and the entry stays as it is. A path-only entry recorded before
// pins existed takes the incoming pin on this first adoption; until it is
// pinned, a change to its start arguments or activation is refused, and the
// person's Connect in Apps replaces it. An applications@3 entry changes only
// through its own revision. The caller holds the lock.
func (d *applicationDirectory) updateSameProgramLocked(ctx context.Context, existing, v wire.ApplicationDescriptor, pin *applicationLegacyPin) wire.ApplicationChange {
	if d.v3 != nil {
		if _, ok := d.v3.entries[v.Name]; ok {
			return appChange(wire.ApplicationOutcomeConflict, "name")
		}
	}
	if !samePrograms(existing.Program, v.Program) {
		return appChange(wire.ApplicationOutcomeConflict, "name")
	}
	if mark, ok := d.unconfirmed[v.Name]; ok && (mark.Removing || mark.Agent != nil) {
		return appChange(wire.ApplicationOutcomeConflict, "name")
	}
	recorded, pinned := d.pins[v.Name]
	switch {
	case pinned && (pin == nil || !sameApplicationLegacyContinuity(recorded, *pin)):
		return appChange(wire.ApplicationOutcomeConflict, "identity")
	case !pinned && !sameActivation(existing.Activation, v.Activation):
		return appChange(wire.ApplicationOutcomeConflict, "identity")
	case !pinned && pin != nil:
		if err := d.setLegacyPinLocked(ctx, v.Name, pin); err != nil {
			return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
		}
	}
	if reflect.DeepEqual(existing, v) {
		return appChange(wire.ApplicationOutcomeApplied, "")
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
	defer d.unlock()
	if d.v3 != nil {
		if _, ok := d.v3.entries[name]; ok {
			return appChange(wire.ApplicationOutcomeConflict, "applications_v3")
		}
	}
	if _, ok := d.descriptors[name]; !ok {
		return appChange(wire.ApplicationOutcomeUnknown, "")
	}
	outcome := d.removeLocked(ctx, name)
	if outcome != wire.ApplicationOutcomeApplied {
		return appChange(outcome, "persistence")
	}
	return appChange(outcome, "")
}

// removeLocked removes the registered entry name, its mark, its presence and
// its activation attempt. The caller holds the directory lock.
func (d *applicationDirectory) removeLocked(ctx context.Context, name string) wire.ApplicationOutcome {
	if d.messageBlock != nil || d.unconfirmed[name].Agent != nil && d.unconfirmed[name].Agent.UpdateReservation != "" {
		if err := d.removeMessageLegacyLocked(ctx, name); err != nil {
			return wire.ApplicationOutcomeUnavailable
		}
	} else {
		next := maps.Clone(d.descriptors)
		delete(next, name)
		if err := d.persistLocked(ctx, next); err != nil {
			return wire.ApplicationOutcomeUnavailable
		}
	}
	// The removed entry's mark and pin go with it; a failed write leaves a
	// mark or pin for a name with no descriptor, which binds nothing.
	_ = d.clearMarkLocked(ctx, name)
	_ = d.setLegacyPinLocked(ctx, name, nil)
	if kept, err := d.readSetupKeptLocked(ctx); err == nil {
		if _, ok := kept[name]; ok {
			delete(kept, name)
			_ = d.writeSetupKeptLocked(ctx, kept)
		}
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
	return wire.ApplicationOutcomeApplied
}

// askConfirmation has the runtime ask the person again whether to keep a
// self-registered entry (AskConfirmation). It needs application.manage; an
// entry without the mark applies without asking.
func (d *applicationDirectory) askConfirmation(ctx context.Context, s rights.Subject, name string) wire.ApplicationChange {
	if out := d.authorized(ctx, s, ActionApplicationManage, "account"); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if !providerName.MatchString(name) {
		return appChange(wire.ApplicationOutcomeInvalid, "application")
	}
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	// An entry Setup's answer kept returns to unconfirmed first: this is the
	// Undo of that Keep (setup_keep.go), and the person is asked about it.
	if _, err := d.undoSetupKeepLocked(ctx, name); err != nil {
		d.unlock()
		return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
	}
	_, ok := d.descriptors[name]
	mark, unconfirmed := d.unconfirmed[name]
	if strings.HasPrefix(mark.Question, messageRemovalQuestion) {
		mark.Question = ""
	}
	unconfirmed = unconfirmed && mark.genericPending()
	if d.v3 != nil {
		if e, exists := d.v3.entries[name]; exists {
			unconfirmed = e.State == "pending" || e.State == "removing"
		}
	}
	d.unlock()
	if !ok {
		return appChange(wire.ApplicationOutcomeUnknown, "")
	}
	if !unconfirmed {
		return appChange(wire.ApplicationOutcomeApplied, "")
	}
	if d.askKeep == nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "questions")
	}
	if err := d.askKeep(name); err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "questions")
	}
	return appChange(wire.ApplicationOutcomeApplied, "")
}

// selfEntry is the unconfirmed entry name with its mark.
func (d *applicationDirectory) selfEntry(name string) (wire.ApplicationDescriptor, selfMark, bool) {
	d.mu.Lock()
	defer d.unlock()
	if d.v3 != nil {
		if e, ok := d.v3.entries[name]; ok {
			return d.applicationV3SelfEntryLocked(name, e)
		}
	}
	v, ok := d.descriptors[name]
	mark, unconfirmed := d.unconfirmed[name]
	if !ok && mark.Removing {
		v = wire.ApplicationDescriptor{Name: name, Program: mark.Program}
	}
	return v, mark, (ok || mark.Removing) && unconfirmed && mark.genericPending()
}

// applicationTitle is the registered title of the application name, empty
// when none is registered.
func (d *applicationDirectory) applicationTitle(name string) string {
	d.mu.Lock()
	defer d.unlock()
	return d.descriptors[name].Title
}

// selfEntries lists the unconfirmed entries with their marks.
func (d *applicationDirectory) selfEntries() map[string]selfMark {
	d.mu.Lock()
	defer d.unlock()
	out := map[string]selfMark{}
	if d.v3 != nil {
		for name, e := range d.v3.entries {
			if _, mark, ok := d.applicationV3SelfEntryLocked(name, e); ok {
				out[name] = mark
			}
		}
	}
	for name, mark := range d.unconfirmed {
		if strings.HasPrefix(mark.Question, messageRemovalQuestion) {
			continue
		}
		if !mark.genericPending() {
			continue
		}
		if _, ok := d.descriptors[name]; ok || mark.Removing {
			out[name] = mark
		}
	}
	return out
}

// ownedLocked reports whether name is unconfirmed, registered by program,
// with question as its registration's question.
func (d *applicationDirectory) ownedLocked(name, program, question string) bool {
	if d.v3 != nil {
		if e, ok := d.v3.entries[name]; ok {
			return (e.State == "pending" || e.State == "removing") && question != "" && e.Question == question && samePrograms(d.v3.files[name].Source.Program, program)
		}
	}
	v, ok := d.descriptors[name]
	mark, unconfirmed := d.unconfirmed[name]
	return ok && unconfirmed && mark.genericPending() && !mark.Removing && samePrograms(v.Program, program) && question != "" && mark.Question == question
}

// setQuestion records question as the one asked for the unconfirmed entry
// name of program. It reports whether the entry was still there.
func (d *applicationDirectory) setQuestion(ctx context.Context, name, program, question string) bool {
	if !d.lock(ctx) {
		return false
	}
	defer d.unlock()
	if d.v3 != nil {
		if _, ok := d.v3.entries[name]; ok {
			return false
		}
	}
	v, ok := d.descriptors[name]
	mark, unconfirmed := d.unconfirmed[name]
	if !ok || !unconfirmed || !mark.genericPending() || mark.Removing || !samePrograms(v.Program, program) {
		return false
	}
	mark.Question = question
	return d.setMarkLocked(ctx, name, mark) == nil
}

// owned reports whether question is the current registration's question
// about the unconfirmed entry name of program.
func (d *applicationDirectory) owned(name, program, question string) bool {
	d.mu.Lock()
	defer d.unlock()
	return d.ownedLocked(name, program, question)
}

// keepOwned marks the entry name confirmed on the person's Keep to question,
// when that is still the question of the entry's current registration. It
// reports whether it changed.
func (d *applicationDirectory) keepOwned(ctx context.Context, name, program, question string) bool {
	if !d.lock(ctx) {
		return false
	}
	defer d.unlock()
	if d.v3 != nil {
		if _, ok := d.v3.entries[name]; ok {
			return d.changeApplicationV3QuestionLocked(ctx, name, program, question, "", "enabled")
		}
	}
	if !d.ownedLocked(name, program, question) {
		return false
	}
	if d.clearMarkLocked(ctx, name) != nil {
		return false
	}
	d.notifyLocked()
	return true
}

// removeOwned removes, on the person's Remove to question, every unconfirmed
// entry of program, when question is still the question of name's current
// registration. It reports whether it removed name.
func (d *applicationDirectory) removeOwned(ctx context.Context, name, program, question string) bool {
	if !d.lock(ctx) {
		return false
	}
	defer d.unlock()
	if d.v3 != nil {
		if _, ok := d.v3.entries[name]; ok {
			return false // @3 person Remove must reserve its Rights-block intent.
		}
	}
	if !d.ownedLocked(name, program, question) {
		return false
	}
	removed := false
	for _, other := range d.unconfirmedOfLocked(program) {
		if d.removeLocked(ctx, other) == wire.ApplicationOutcomeApplied && other == name {
			removed = true
		}
	}
	return removed
}

// persistLocked uses the shared CAS provider for synced atomic replacement.
// Another writer changing the same root is refused instead of losing its update.
func (d *applicationDirectory) persistLocked(ctx context.Context, next map[string]wire.ApplicationDescriptor) error {
	var peers *messagePeerFacts
	if prior := d.roles.Load(); prior != nil {
		peers = prior.messagePeers
	}
	d.roles.Store(nil)
	legacy := maps.Clone(next)
	if d.v3 != nil {
		for name := range d.v3.entries {
			delete(legacy, name)
		}
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		return err
	}
	if err = rawcas.ChangeLimitContext(ctx, filepath.Join(d.dir, "descriptors.json"), d.store.MaxBytes, func(current []byte) ([]byte, error) {
		if (current == nil) != (d.base.Data == nil) || !bytes.Equal(current, d.base.Data) {
			return nil, rawcas.ErrMoved
		}
		currentLegacy, e := decodeLegacyApplications(current)
		if e != nil {
			return nil, e
		}
		marks, _, e := d.readSelfMarksAtDescriptorLocked(ctx)
		if e != nil {
			return nil, e
		}
		for name, mark := range marks {
			if mark.Agent != nil && mark.Agent.UpdateReservation != "" && !reflect.DeepEqual(currentLegacy[name], legacy[name]) {
				return nil, errors.New("applications: connection update reserved")
			}
		}
		for name, mark := range marks {
			if mark.Removing {
				if strings.HasPrefix(mark.Question, messagePublicationQuestion) {
					candidate, exists := legacy[name]
					actual, e := legacyApplicationRemovalRevision(candidate, mark)
					if !exists || e != nil || actual != mark.RemovalRevision {
						return nil, errors.New("applications: publication intent changed")
					}
				}
				if descriptor, active := currentLegacy[name]; active {
					before, e := legacyApplicationRemovalRevision(descriptor, mark)
					if e != nil || before != mark.RemovalRevision {
						return nil, errors.New("applications: corrupt Remove intent")
					}
					replacement, exists := legacy[name]
					if !exists {
						return nil, errors.New("applications: Remove intent reserved")
					}
					after, e := legacyApplicationRemovalRevision(replacement, mark)
					if e != nil || before != after {
						return nil, errors.New("applications: Remove intent reserved")
					}
				}
			}
		}
		if d.v3 == nil {
			b, e := rawcas.ReadLimitContext(ctx, filepath.Join(filepath.Dir(d.dir), "applications-v3", "registrations", "index.json"), applicationV3IndexLimit)
			if e != nil {
				return nil, e
			}
			if b != nil {
				return nil, errors.New("applications@3: owner unavailable")
			}
		} else {
			if err := d.v3.readLocked(ctx); err != nil {
				return nil, err
			}
			if len(legacy)+len(d.v3.entries) > 64 {
				return nil, errors.New("applications: shared capacity")
			}
			for name, entry := range d.v3.entries {
				if _, ok := legacy[name]; ok {
					return nil, errors.New("applications: applications@3 collision")
				}
				if entry.State == "pending" || entry.State == "removing" {
					for n, mark := range d.unconfirmed {
						if descriptor, ok := legacy[n]; ok && mark.pendingOwnership() && applicationRegistrationPrograms(mark.boundProgram(descriptor), d.v3.files[name].Source.Program, mark.ProgramKey, d.v3.files[name].Source.FileKey) {
							return nil, errors.New("applications: shared unconfirmed")
						}
					}
				}
			}
		}
		return raw, nil
	}); err != nil {
		return err
	}
	d.base = cas.Value{Data: raw}
	if d.v3 != nil {
		if err := d.projectApplicationV3Locked(legacy); err != nil {
			return err
		}
		roles, err := d.projectRolesLocked()
		if err != nil {
			return err
		}
		d.publishRoles(roles, peers)
		return nil
	}
	d.descriptors = next
	return nil
}

var applicationContextName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

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
		if !applicationContextName.MatchString(v.Name) || seen[v.Name] || len(v.Title) > 256 || len(v.Revision) > 256 {
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
			d.recordPresenceEndLocked(l, applicationPresenceLapsed)
			delete(d.instances, id)
		}
	}
	d.expireNativeInboxesLocked()
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
	defer d.unlock()
	// Ownership is decided before anything changes: a name the caller's
	// program does not own, registered or not, answers forbidden alike and
	// touches no entry and no activation attempt, so another program can
	// neither learn which names exist nor end someone's start.
	v, ok := d.descriptors[p.Application]
	if !ok || !samePrograms(v.Program, s.Program) {
		return appChange(wire.ApplicationOutcomeForbidden, "")
	}
	d.expireLocked()
	id := p.Instance
	var agentAdmission *applicationAgentAdmission
	if id != "" {
		old, ok := d.instances[id]
		if !ok {
			return appChange(wire.ApplicationOutcomeStale, "")
		}
		if old.application != p.Application || old.owner.Account != s.Account || !samePrograms(old.owner.Program, s.Program) || old.session != session {
			return appChange(wire.ApplicationOutcomeForbidden, "")
		}
		agentAdmission = old.agentAdmission
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
	d.instances[id] = applicationLease{application: p.Application, owner: s, session: session, value: wire.ApplicationInstance{Instance: id, Interfaces: interfaces, Contexts: contexts, ExpiresUnixMs: deadline.UnixMilli()}, deadline: deadline, agentAdmission: agentAdmission}
	// Removing a presence context ends its inbox in this same edit. A later
	// re-addition cannot revive the earlier native ownership or status revision.
	d.expireNativeInboxesLocked()
	if p.Instance == "" {
		d.recordStartLocked(ctx, p.Application, session, id, v)
	}
	// Native inbox removal above signals the mailbox. A presence heartbeat
	// alone does not change inbox ownership or a message's admission.
	d.notifyDirectoryLocked()
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
	defer d.unlock()
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
		d.unlock()
		return
	}
	// Issuing the launch while this generation is current is the activation's
	// linearization point. The platform call runs outside the directory lock so
	// a stalled desktop service cannot block removal, observation or presence.
	launched := make(chan error, 1)
	go func() {
		launched <- d.launchRegisteredRecipe(context.Background(), descriptor, func(context.Context) error { return nil })
	}()
	d.unlock()
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
			d.unlock()
			return
		}
		if instance := d.readyInstanceLocked(application, descriptor.Activation.Readiness, attempt.session); instance != "" {
			d.unlock()
			d.finishActivation(application, attempt, wire.ApplicationActivationResult{Outcome: wire.ApplicationActivationOutcomeReady, Instance: instance, Started: true}, false)
			return
		}
		changed := d.changed
		d.unlock()
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
	if d.v3 != nil {
		if _, ok := d.v3.entries[application]; ok {
			d.unlock()
			return activationResult(wire.ApplicationActivationOutcomeDisabled, "applications_v3")
		}
	}
	descriptor, exists := d.descriptors[application]
	if !exists {
		d.unlock()
		return activationResult(wire.ApplicationActivationOutcomeUnknown, "")
	}
	if descriptor.Activation == nil {
		d.unlock()
		return activationResult(wire.ApplicationActivationOutcomeDisabled, "")
	}
	attempt := d.attempts[application]
	if attempt != nil && attempt.session != session {
		d.unlock()
		return activationResult(wire.ApplicationActivationOutcomeForbidden, "session")
	}
	if attempt != nil && attempt.generation != d.generations[application] {
		d.unlock()
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
		d.unlock()
		return wire.ApplicationActivationResult{Outcome: wire.ApplicationActivationOutcomeReady, Instance: instance}
	}
	attempt = d.attempts[application]
	if attempt == nil {
		if len(d.attempts) >= 64 {
			d.unlock()
			return activationResult(wire.ApplicationActivationOutcomeUnavailable, "capacity")
		}
		attempt = &applicationActivationAttempt{done: make(chan struct{}), generation: d.generations[application], session: session}
		d.attempts[application] = attempt
		go d.runActivation(application, descriptor, attempt)
	}
	d.unlock()
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
	defer d.unlock()
	d.expireLocked()
	l, ok := d.instances[id]
	if !ok {
		return appChange(wire.ApplicationOutcomeStale, "")
	}
	if l.application != name || l.owner.Account != s.Account || !samePrograms(l.owner.Program, s.Program) || l.session != session {
		return appChange(wire.ApplicationOutcomeForbidden, "")
	}
	d.recordPresenceEndLocked(l, applicationPresenceWithdrawn)
	delete(d.instances, id)
	d.expireNativeInboxesLocked()
	d.notifyLocked()
	return appChange(wire.ApplicationOutcomeApplied, "")
}

func (d *applicationDirectory) snapshot(ctx context.Context, s rights.Subject) (wire.ApplicationPage, <-chan struct{}, time.Duration) {
	if !d.lock(ctx) {
		return wire.ApplicationPage{Outcome: wire.ApplicationOutcomeUnavailable, Applications: []wire.ApplicationEntry{}}, nil, time.Second
	}
	defer d.unlock()
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
		mark, unconfirmed := d.unconfirmed[name]
		unconfirmed = unconfirmed && mark.genericPending()
		if d.v3 != nil {
			if e, ok := d.v3.entries[name]; ok {
				unconfirmed = e.State == "pending" || e.State == "removing"
			}
		}
		entry := wire.ApplicationEntry{Scope: wire.ScopeLocal, Descriptor: v, Instances: []wire.ApplicationInstance{}, SelfRegistered: unconfirmed}
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
	//unchecked: raw only feeds a size guard and a change-cursor hash below, never a returned payload; its wire.ApplicationEntry fields are plain marshalable data
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
	var page wire.ApplicationPage
	var expiry time.Time
	outcome, err := d.gate.Wait(ctx, time.Duration(waitMS)*time.Millisecond, watch.Probe{
		Read: func() (bool, error) {
			var until time.Duration
			page, _, until = d.snapshot(ctx, s)
			expiry = time.Now().Add(until)
			if page.Outcome == wire.ApplicationOutcomePage && cursor != "" && !strings.HasPrefix(cursor, d.epoch+".") {
				page.Outcome = wire.ApplicationOutcomeStale
			}
			return page.Outcome != wire.ApplicationOutcomePage || page.Cursor != cursor, nil
		},
		// The snapshot decides the caller's permission on every read, so the
		// read is its own recheck.
		Recheck: func() error { return nil },
		// A grant's expiry changes the page by itself (WATCH-H1).
		Deadline: func() time.Time { return expiry },
	})
	if err != nil || outcome == watch.Busy || outcome == watch.Failed {
		return wire.ApplicationPage{Outcome: wire.ApplicationOutcomeUnavailable, Applications: []wire.ApplicationEntry{}}
	}
	return page
}
