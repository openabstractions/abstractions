package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/manifest"
	"path/filepath"
	"sync"

	asks "github.com/openabstractions/abstraction-asks/go"
	downloadrequest "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

var errApplicationV3NotFound = errors.New("application not found")

// A nil hold is deliberately a retained declaration read, with no eligibility
// claim. Every effect and protected reply separately calls fresh revalidation.
func (g *applicationV3Gateway) parse(raw []byte, program string, held *heldApplicationManifest) (*registrationwire.Manifest, error) {
	return g.parseManifest(raw, program, held, held == nil)
}

// Only a retained registration whose original bytes just passed native custody
// may use the private spelling projection with a live held image.
func (g *applicationV3Gateway) parseRetained(raw []byte, program string, held *heldApplicationManifest) (*registrationwire.Manifest, error) {
	return g.parseManifest(raw, program, held, true)
}

// heldDeclaration reads a revalidated live hold as its kept registration: the
// retained-spelling projection and the digest the registration recorded. Every
// recheck and owned start of a kept entry uses it, so an entry kept before a
// member rename opens the same way it lists.
func (g *applicationV3Gateway) heldDeclaration(held *heldApplicationManifest, program string) (*registrationwire.Manifest, string, error) {
	fresh, err := g.parseRetained(held.Bytes, program, held)
	if err != nil {
		return nil, "", err
	}
	canonical := applicationV3PriorCanonical(registrationwire.EncodeManifest(fresh), held.Bytes)
	sum := sha256.Sum256(canonical)
	return fresh, hex.EncodeToString(sum[:]), nil
}

func (g *applicationV3Gateway) parseManifest(raw []byte, program string, held *heldApplicationManifest, retained bool) (*registrationwire.Manifest, error) {
	if retained && held != nil && !bytes.Equal(raw, held.Bytes) {
		return nil, errors.New("retained signed bytes changed")
	}
	owners := manifest.ManifestOwners{
		ValidateServer: func(*registrationwire.Manifest) error {
			if held == nil {
				return nil
			}
			if !heldSigned(held) && held.ImageSHA256 == "" || identity.SharedInterpreterProgram(program, held.Code) {
				return errors.New("signed_source")
			}
			return nil
		},
		PrepareResource: func(action, input string) (bool, error) {
			if g.policy == nil || g.policy.policy == nil || g.policy.policy.Definitions == nil {
				return false, errors.New("request owner unavailable")
			}
			catalog := g.policy.policy.Definitions
			definition, ok := catalog.Lookup(action)
			if !ok || !definition.Served {
				return false, errors.New("request action unavailable")
			}
			// Existing legacy definitions accept a public exact string. Other
			// input profiles require their own actual preparation adapter and
			// refuse here rather than treat decoded claims as prepared evidence.
			var resource string
			if definition.ResourceProfile != rights.LegacyExactResourceProfile || json.Unmarshal([]byte(input), &resource) != nil || !catalog.ValidResource(action, resource) {
				return false, errors.New("request input unsupported")
			}
			facts := grants.TierFacts{}
			if g.asks != nil {
				facts = g.asks.tierFacts
			}
			return grants.FirstUseTier(action, resource, facts) != asks.TierRoutine, nil
		},
		ValidateFile: func(request *downloadrequest.Request) error {
			return downloadserve.ValidateRequest(downloadrequest.Encode(request))
		},
	}
	if retained {
		raw, _ = applicationV3RetainedManifest(raw)
	}
	return manifest.ParseApplicationManifest(raw, owners)
}

func (g *applicationV3Gateway) decoder(raw []byte, program string, held *heldApplicationManifest) (legacy.ApplicationDescriptor, string, error) {
	manifest, err := g.parse(raw, program, held)
	if err != nil {
		return legacy.ApplicationDescriptor{}, "", err
	}
	canonical := registrationwire.EncodeManifest(manifest)
	if held == nil {
		if _, prior := applicationV3RetainedManifest(raw); prior {
			canonical = applicationV3PriorCanonical(canonical, raw)
		}
	}
	sum := sha256.Sum256(canonical)
	return legacy.ApplicationDescriptor{Name: manifest.Name, Title: manifest.Title, Program: program}, hex.EncodeToString(sum[:]), nil
}

func (g *applicationV3Gateway) kept(view applicationV3RegistrationView, peer *identity.Peer) (*applicationV3Kept, error) {
	manifest, err := g.parse(view.Raw, view.Program, nil)
	if err != nil {
		return nil, err
	}
	k := &applicationV3Kept{manifest: manifest, program: view.Program, revision: view.Revision, digest: view.ManifestDigest, keepIdentity: view.KeepIdentity, state: view.State, live: map[string]bool{}}
	for _, instance := range view.Instances {
		k.live[instance.Instance] = true
	}
	var mu sync.Mutex
	var hold *heldApplicationManifest
	closed := false
	k.close = func() error {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		if hold != nil {
			return hold.Close()
		}
		return nil
	}
	k.recheck = func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return errors.New("manifest hold closed")
		}
		current, err := g.directory.revalidateApplicationV3(ctx, peer, view.Name, view.Revision)
		if err != nil {
			return err
		}
		_, digest, err := g.heldDeclaration(current, view.Program)
		if err != nil {
			current.Close()
			return err
		}
		if digest != view.ManifestDigest {
			current.Close()
			return errors.New("manifest revision changed")
		}
		if hold != nil {
			hold.Close()
		}
		hold = current
		return current.Recheck(ctx)
	}
	return k, nil
}

func (g *applicationV3Gateway) loadDeclaration(ctx context.Context, peer *identity.Peer, name string) (*applicationV3Kept, error) {
	view, found, err := g.directory.applicationV3Declaration(ctx, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errApplicationV3NotFound
	}
	return g.kept(view, peer)
}

func (g *applicationV3Gateway) listDeclarations(ctx context.Context, peer *identity.Peer) ([]*applicationV3Kept, error) {
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return nil, err
	}
	views, _, _, outcome := g.directory.applicationV3Declarations(ctx, subject)
	if outcome != legacy.ApplicationOutcomePage {
		return nil, errors.New("directory unavailable")
	}
	result := []*applicationV3Kept{}
	for _, view := range views {
		// One declaration that no longer parses leaves the others listed.
		k, err := g.kept(view, peer)
		if err != nil {
			if g.report != nil {
				g.report(fmt.Errorf("applications@3: declaration %s skipped: %w", view.Name, err))
			}
			continue
		}
		result = append(result, k)
	}
	return result, nil
}

func applicationV3NativeChange(change applicationV3RegistrationChange) applications.Change {
	outcome, known := applications.ParseOutcome(change.Outcome.String())
	if !known {
		outcome = applications.OutcomeUnavailable
	}
	return applications.Change{Outcome: outcome, Revision: change.Revision, Reason: change.Reason}
}

func (g *applicationV3Gateway) register(ctx context.Context, c applicationV3Caller, request applications.RegisterRequest) applications.Change {
	if !c.valid(g.directory.owner) {
		return applications.Change{Outcome: applications.OutcomeForbidden, Reason: "caller"}
	}
	return applicationV3NativeChange(g.directory.registerApplicationV3(ctx, c.subject, c.peer, request.Program, request.ExpectedRevision))
}

func (g *applicationV3Gateway) change(ctx context.Context, c applicationV3Caller, name, revision, state string) applications.Change {
	if !c.valid(g.directory.owner) {
		return applications.Change{Outcome: applications.OutcomeForbidden, Reason: "caller"}
	}
	return applicationV3NativeChange(g.directory.changeApplicationV3(ctx, c.subject, name, revision, state))
}

func enableApplicationV3(ctx context.Context, h *applicationsHost, policy *runtimeRights, questions *runtimeAsks, sink logging.Sink) error {
	policy.requireApplicationRoles()
	if h == nil || policy == nil || policy.policy == nil {
		return errors.New("applications@3: policy unavailable")
	}
	ready := false
	defer func() {
		if !ready {
			if policy.applications != nil {
				policy.applications.roles.Store(nil)
			}
			policy.applications = nil
		}
	}()
	store, err := openApplicationV3Receipts(filepath.Dir(h.directory.dir))
	if err != nil {
		return err
	}
	g := &applicationV3Gateway{directory: h.directory, receipts: store, policy: policy, asks: questions, epoch: h.directory.epoch, owned: map[string]*ownedApplicationSession{}, starting: map[string]chan struct{}{}, start: startOwnedApplication}
	h.directory.roleDecode = func(raw []byte, program string) (bool, error) {
		manifest, err := g.parse(raw, program, nil)
		if err != nil {
			return false, err
		}
		return manifest.ScriptHost, nil
	}
	policy.applications = h.directory
	if err = h.directory.attachApplicationV3(ctx, g.decoder); err != nil {
		return err
	}
	g.load = g.loadDeclaration
	g.list = g.listDeclarations
	g.consent = g.bundleConsent
	g.report = h.report
	g.audit, g.auditCheck, err = applicationV3AuditWriter(sink, h.report)
	if err != nil {
		return err
	}
	if g.audit == nil {
		return errors.New("applications@3: audit unavailable")
	}
	h.directory.v3Changed = g.invalidate
	h.v3 = g
	ready = true
	return nil
}
