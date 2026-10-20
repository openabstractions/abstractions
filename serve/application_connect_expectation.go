package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	rawcas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The person's Connect click in the Panel is the Keep (architect ruling
// 2026-11-11, APPLICATIONS.md "Connect is the Keep"). The Panel's Connect
// handler records one expectation through ExpectConnect on the operator-only
// abstraction.applications/operator@1: the app name, its program path and the
// SHA-256 of the exact image the Panel installed. Admission is the caller's
// same-account Program proof and the operator check narrowed to the Panel
// program; the runtime holds the record in memory for ten minutes. The
// first applied own register-self for the name inside the window consumes
// it. When that registration comes from the same normalized path and its
// held image hashes to the pin, the runtime keeps exactly that registration,
// bound to its revision, with decision source panel_connect. Any other
// registration waits for the person's Keep question, which the Panel raises
// (AskConfirmation).

// connectExpectationWindow is how long one Connect click stays good.
const connectExpectationWindow = 10 * time.Minute

// connectDecisionSource names a Keep the person's Connect click made.
const connectDecisionSource = "panel_connect"

// connectQuestionPrefix marks the private question token the Keep of a
// matched Connect binds for its one conditional edit. No Book record carries
// it: a token left by an interrupted Keep reads as a question that is gone,
// and the person is asked (resume, AskConfirmation).
const connectQuestionPrefix = "panel-connect:"

// connectKeptFile records each Keep a Connect click made, by app name.
const connectKeptFile = "connect-kept.json"

const connectKeptLimit = 64 << 10

var connectPin = regexp.MustCompile(`^[0-9a-f]{64}$`)

type connectExpectation struct {
	id      string
	program string
	sha256  string
	expires time.Time
}

// connectExpectations holds the pending Connect clicks by app name.
type connectExpectations struct {
	mu      sync.Mutex
	pending map[string]connectExpectation
}

// connectKept is one Keep a Connect click made.
type connectKept struct {
	Decision string    `json:"decision"`
	Receipt  string    `json:"receipt,omitempty"`
	At       time.Time `json:"at"`
	Program  string    `json:"program"`
	SHA256   string    `json:"sha256"`
	// FileKey is the file identity of the image the Connect kept, so a signed
	// image, which records no SHA-256 pin, is recognised as the same file.
	FileKey string `json:"file_key,omitempty"`
	// HoldsWritten records that the Keep's hold rules were written for this
	// record, so a hold the person removed is not written again. A new
	// record starts without it (writeKeptHolds).
	HoldsWritten bool `json:"holds_written,omitempty"`
}

func (d *applicationDirectory) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// panelForConnect uses the bound Panel proof for runtime directories.
// Inert directory fixtures can supply their own subject predicate.
func (d *applicationDirectory) panelForConnect(ctx context.Context, peer *identity.Peer, program string) bool {
	if peer != nil || d.connectPeer != nil {
		return peer != nil && d.connectPeer != nil && d.connectPeer(ctx, peer)
	}
	return d.connectPanel != nil && d.connectPanel(program)
}

// expectConnect records the Panel's Connect click for one app: the operator
// admission of abstraction.applications/operator@1, the Panel program of
// this account (connectPanel) holding application.manage.
func (d *applicationDirectory) expectConnect(ctx context.Context, s rights.Subject, request applications.ConnectExpectation) wire.ApplicationChange {
	if s.Account != d.owner || s.Program == "" || d.connectPanel == nil || !d.connectPanel(s.Program) {
		return appChange(wire.ApplicationOutcomeForbidden, "caller")
	}
	if out := d.authorized(ctx, s, ActionApplicationManage, "account"); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	program := identity.NormalizeSubjectProgram(request.Program)
	if !providerName.MatchString(request.Application) || program == "" || !filepath.IsAbs(program) || !connectPin.MatchString(request.SHA256) {
		return appChange(wire.ApplicationOutcomeInvalid, "expectation")
	}
	d.connects.mu.Lock()
	defer d.connects.mu.Unlock()
	now := d.clock()
	prior := d.connects.pending[request.Application]
	if now.Before(prior.expires) && prior.sha256 == request.SHA256 && prior.program == program {
		return appChange(wire.ApplicationOutcomeApplied, "")
	}
	id, err := applicationToken()
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "expectation")
	}
	if d.connects.pending == nil {
		d.connects.pending = map[string]connectExpectation{}
	}
	d.connects.pending[request.Application] = connectExpectation{id: id, program: program, sha256: request.SHA256, expires: now.Add(connectExpectationWindow)}
	return appChange(wire.ApplicationOutcomeApplied, "")
}

// takeConnect consumes the expectation for name and reports whether program
// and the held image's SHA-256 match it inside its window. A mismatch uses it
// up too: after the person's answer to the fallback question, a later
// registration of the pinned image waits for a new Connect click.
func (d *applicationDirectory) takeConnect(name, program, sha256 string) (connectExpectation, bool) {
	return d.takeConnectExpected(name, program, sha256, "")
}

func (d *applicationDirectory) takeConnectExpected(name, program, sha256, expectedID string) (connectExpectation, bool) {
	d.connects.mu.Lock()
	defer d.connects.mu.Unlock()
	e, ok := d.connects.pending[name]
	if !ok || expectedID != "" && e.id != expectedID {
		return connectExpectation{}, false
	}
	delete(d.connects.pending, name)
	if !d.clock().Before(e.expires) {
		return connectExpectation{}, false
	}
	if sha256 == "" || e.sha256 != strings.ToLower(sha256) || !identity.SameSubjectProgram(e.program, identity.NormalizeSubjectProgram(program)) {
		return connectExpectation{}, false
	}
	return e, true
}

// matchConnect keeps name's waiting registration at revision, the one whose
// program and image it checked, when it matches the Panel's Connect click
// (takeConnect). It runs after every applied own registration, the unchanged
// re-registration of a waiting entry included.
func (d *applicationDirectory) matchConnect(ctx context.Context, name, revision, program, sha256, fileKey string) bool {
	if d.keepConnected == nil {
		return false
	}
	e, ok := d.takeConnect(name, program, sha256)
	if !ok {
		return false
	}
	if !d.keepConnected(name, revision) {
		return false
	}
	if err := d.recordConnectKeep(ctx, name, connectKept{Decision: connectDecisionSource, At: d.clock().UTC(), Program: e.program, SHA256: e.sha256, FileKey: fileKey}); err != nil && d.connectReport != nil {
		d.connectReport(err)
	}
	// The record makes this registration a kept OA integration image: it
	// holds the card and memory (registry_v3_keep_hold.go).
	d.writeKeptHolds(ctx)
	return true
}

// installerUpdateSource names the record an installer's re-pin writes.
const installerUpdateSource = "installer_update"

// matchInstallerUpdate records the installing operator's re-pin of a kept
// entry to OA's release pin (architect R35): Setup's step, which holds the
// build's integration pin, records the expectation through ExpectConnect and
// then re-pins the entry the person kept, by any route, to the image it
// installed. When that re-pin's program and held image match the
// expectation, the runtime writes the same record a Connect click writes,
// with source installer_update, and the entry's holds follow. A re-pin of
// another image records nothing and uses the expectation up.
func (d *applicationDirectory) matchInstallerUpdate(ctx context.Context, name, program, sha256, expectedID string) bool {
	if expectedID == "" {
		return false
	}
	// The registry commit is the authority. Retire only this expectation;
	// the durable receipt can still reconcile after it or the process ends.
	d.takeConnectExpected(name, program, sha256, expectedID)

	d.writeKeptHolds(ctx)
	return true
}

func (d *applicationDirectory) recordConnectKeep(ctx context.Context, name string, keep connectKept) error {
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	return d.recordConnectKeepLocked(ctx, name, keep)
}

func (d *applicationDirectory) recordConnectKeepLocked(ctx context.Context, name string, keep connectKept) error {
	path := filepath.Join(d.dir, connectKeptFile)
	return rawcas.ChangeLimitContext(ctx, path, connectKeptLimit, func(current []byte) ([]byte, error) {
		kept := map[string]connectKept{}
		if current != nil && json.Unmarshal(current, &kept) != nil || kept == nil {
			kept = map[string]connectKept{}
		}
		if prior, ok := kept[name]; ok && keep.Receipt != "" && (prior.Receipt == keep.Receipt || prior.At.After(keep.At)) {
			return current, nil
		}
		next := maps.Clone(kept)
		next[name] = keep
		raw, err := json.Marshal(next)
		for err == nil && len(raw) > connectKeptLimit && len(next) > 1 {
			for other := range next {
				if other != name {
					delete(next, other)
					break
				}
			}
			raw, err = json.Marshal(next)
		}
		return raw, err
	})
}

// keepConnected records the Keep that the person's Connect click made of
// name's registration at revision: the same conditional edit a Keep answer
// makes, bound to a private question token instead of a Book question. The
// token binds only while the entry is still at revision, the registration
// whose image matched; a newer registration keeps nothing and waits for the
// Keep question. A Keep question already open about it is retired.
func (k *selfRegisteredQuestions) keepConnected(name, revision string) bool {
	if revision == "" {
		return false
	}
	name = k.selfName(name)
	v, mark, ok := k.selfEntry(name)
	if !ok || mark.Removing || mark.registrationRevision() != revision {
		return false
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return false
	}
	token := connectQuestionPrefix + hex.EncodeToString(raw[:])
	program, earlier := mark.boundProgram(v), mark.Question
	if !k.setQuestion(name, program, token, revision) {
		return false
	}
	if !k.keepOwned(name, program, token) {
		// The open question, if any, stays the entry's question.
		if earlier != "" && !k.setQuestion(name, program, earlier, revision) {
			k.a.report(errors.New("applications: a Connect Keep failed and the open Keep question could not be restored"))
		}
		return false
	}
	if earlier != "" && !strings.HasPrefix(earlier, connectQuestionPrefix) {
		if _, _, err := k.a.book.RetirePendingApplicationAuthorized(earlier, func() error { return nil }); err != nil {
			k.a.report(err)
		}
	}
	return true
}
