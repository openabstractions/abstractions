package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-identity/go/budget"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	download "github.com/openabstractions/abstraction-download/go"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// ModelCredentialConsumer is the consumer contract model lookup names.
const modelCredentialConsumer = "abstraction.model/resolver@1"

// runtimeCredentials composes abstraction.credentials into the user runtime:
// the holder on the account's platform store, the rights decision policy it
// asks, the operator programs that administer both, and the in-process appliers
// download execution and model lookup use. No program outside the runtime is
// designated an enforcer, so applier@1 is not published over IPC.
type runtimeCredentials struct {
	*runtimeRights
	holder   *credentials.Holder
	host     *credservice.Host
	endpoint string
	// release lets go of the platform store once the holder stops serving
	// (credentials OpenPlatformStore); nil holds nothing.
	release func()
	// hostedCredential names the credential a hosted model server declares
	// (composeInference reads the router's hosts); nil names none.
	hostedCredential func(host string) string
}

// credentialsState is the runtime's private credentials and rights state, or ""
// when the runtime selected explicit endpoints without a state directory.
func credentialsState(options runtimeFlags) (string, error) {
	if options.stateDir != "" {
		return options.stateDir, nil
	}
	for _, item := range endpointFlags(options) {
		if item.value != "" {
			return "", nil
		}
	}
	return runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

// credentialsEndpoints follow the runtime's own endpoint selection: installed
// names, names derived from --isolated, or suffixes of an explicit --endpoint.
func credentialsEndpoints(options runtimeFlags) (credentialsEndpoint, rightsEndpoint, namespace string, err error) {
	switch {
	case options.isolated != "":
		if credentialsEndpoint, err = options.companionEndpoint("credentials-v1", "credentials", "credentials"); err != nil {
			return "", "", "", err
		}
		rightsEndpoint, err = options.companionEndpoint("rights-authorization-v1", "rights", "rights")
		return credentialsEndpoint, rightsEndpoint, "oa-isolated-" + options.isolated, err
	case options.endpoint != "" && !options.xpc:
		sum := sha256.Sum256([]byte(options.endpoint))
		return options.endpoint + "-credentials", options.endpoint + "-rights", "oa-runtime-" + hex.EncodeToString(sum[:6]), nil
	}
	if credentialsEndpoint, err = options.defaultEndpoint("credentials-v1"); err != nil {
		return "", "", "", err
	}
	rightsEndpoint, err = options.defaultEndpoint("rights-authorization-v1")
	return credentialsEndpoint, rightsEndpoint, credentials.DefaultNamespace, err
}

// operatorPrograms are the runtime's own executable and its installed operator
// siblings: the command line, its windowless link and the Abstraction Panel,
// the programs holding holder.manage, holder.read and rights operator authority
// by installation (research/feedback-archive-2026-09-24/credentials-holder-design.md, operator tools).
// An isolated runtime also names the one program its operator passed with
// --operator-program (isolatedOperatorProgram).
// Each path is canonicalized (identity.CanonicalProgramPath) so a runtime
// launched through a short DOS 8.3 alias still recognizes the same operator
// programs a caller's peer identity, also canonicalized, is compared against
// in authorizeOperator.
func operatorPrograms(options runtimeFlags) ([]string, panelRoster, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, panelRoster{}, err
	}
	extra, err := isolatedOperatorProgram(options)
	if err != nil {
		return nil, panelRoster{}, err
	}
	operators, panels := operatorSiblings(identity.CanonicalProgramPath(filepath.Clean(exe)), extra)
	return operators, panels, nil
}

// isolatedOperatorProgram is the canonical path of --operator-program, or ""
// when none was given. A Panel built from source has no installed sibling
// name, so an isolated runtime names it explicitly (live walkthrough
// 2026-10-02, D7). The installed runtime never takes one: the program is
// accepted only by an isolated runtime (--isolated, whose endpoints are
// derived from its name, with its own --state-dir), never with a state
// directory that is the account's default one by file identity
// (sameFolder), and never in installed-parent mode. Its rights
// policy and credentials namespace are then that runtime's own, so the
// program administers nothing the installed runtime holds.
func isolatedOperatorProgram(options runtimeFlags) (string, error) {
	return isolatedOperatorProgramWithState(options, true)
}

func isolatedOperatorProgramWithState(options runtimeFlags, create bool) (string, error) {
	path := options.operatorProgram
	if path == "" {
		return "", nil
	}
	if options.stateDir == "" || options.isolated == "" || options.supervised {
		return "", fmt.Errorf("runtime: --operator-program is accepted only by an isolated runtime (%s); the installed runtime's operators are the programs installed beside it", isolatedRuntimeUsage)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("runtime: --operator-program %q must be the absolute path of the executable", path)
	}
	// Neither the program nor the state directory is opened before the path
	// owner admits it (identity.LocalPath): a share, its \\?\UNC\ and \??\
	// forms, a device path and a mapped network drive in any spelling are
	// refused unopened, since opening a share sends the account's
	// credentials to its host.
	if err := localPath(path); err != nil {
		return "", fmt.Errorf("runtime: --operator-program %q must be on a local drive: %w", path, err)
	}
	if err := localPath(options.stateDir); err != nil {
		return "", fmt.Errorf("runtime: --operator-program needs a state directory on a local drive, not the share, device or network drive %q: %w", options.stateDir, err)
	}
	if err := resetSafePath(context.Background(), options.stateDir); err != nil {
		return "", fmt.Errorf("runtime: --operator-program cannot admit state directory: %w", err)
	}
	info, err := statPath(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("runtime: --operator-program %q names no executable file", path)
	}
	// A symbolic link in the state directory's ancestry can still lead to a
	// share; the folder it resolves to must be local as well.
	if resolved, err := resolvePath(options.stateDir); err != nil {
		return "", fmt.Errorf("runtime: --operator-program cannot resolve the state directory %q: %w", options.stateDir, err)
	} else if err := localPath(resolved); err != nil {
		return "", fmt.Errorf("runtime: --operator-program needs a state directory on a local drive; %q resolves to %q: %w", options.stateDir, resolved, err)
	}
	installed, err := runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", fmt.Errorf("runtime: --operator-program needs the installed runtime's state directory to tell it apart: %w", err)
	}
	// Checked before the folder is created, so a refusal leaves nothing
	// behind, and again by file identity once it exists. The check catches
	// another name for the installed runtime's folder: an 8.3 short name, a
	// junction or symbolic link, a \\?\ prefix, or a link in either path's
	// ancestry. It does not catch a link planted inside
	// the state folder, or one made after this check; making either takes a
	// process of this account, which can already act as this account; the
	// opening paragraph of abstraction-credentials CONTRACT.md leaves such a
	// process out of scope.
	if err := distinctState(options.stateDir, installed); err != nil {
		return "", err
	}
	if !create {
		return identity.CanonicalProgramPath(filepath.Clean(path)), nil
	}
	if err := os.MkdirAll(options.stateDir, 0o700); err != nil {
		return "", fmt.Errorf("runtime: --operator-program needs a state directory of its own: %w", err)
	}
	if err := distinctState(options.stateDir, installed); err != nil {
		return "", err
	}
	return identity.CanonicalProgramPath(filepath.Clean(path)), nil
}

// localPath is identity.LocalPath and statPath os.Stat, the checks and the
// open of a caller's path here; tests stand in a mapped drive and count
// opens.
var (
	localPath = identity.LocalPath
	statPath  = os.Stat
)

// distinctState refuses a state directory that is the installed runtime's.
func distinctState(state, installed string) error {
	same, err := sameFolder(state, installed)
	if err != nil {
		return fmt.Errorf("runtime: --operator-program cannot tell the state directory from the installed runtime's: %w", err)
	}
	if same {
		return errors.New("runtime: --operator-program needs a state directory of its own, not the installed runtime's")
	}
	return nil
}

// sameFolder reports whether a and b name one folder. When both exist it
// compares file identity (identity.SameFile), so a short 8.3 name, a
// junction or symbolic link and a \\?\ prefix compare equal. A side off a
// local drive, such as a loopback share, is never opened (identity
// LocalPath): sameFolder answers identity's refusal, so distinctState
// refuses it. When either is absent it compares resolved paths: each one's
// nearest existing ancestor with links resolved, joined to the missing
// rest. That decides before creation, when both are absent; with one present
// and one absent the resolved paths cannot match, since a match would make
// the absent one exist.
// The names of the folders still to be created decide as identity.SamePathName
// compares them: a collision check before placement.
func sameFolder(a, b string) (bool, error) {
	same, err := identity.SameFile(a, b)
	if err == nil {
		return same, nil
	}
	if errors.Is(err, identity.ErrNotLocalPath) {
		return false, err
	}
	_, errA := statPath(a)
	_, errB := statPath(b)
	for _, err := range []error{errA, errB} {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	resolvedA, err := resolvePath(a)
	if err != nil {
		return false, err
	}
	resolvedB, err := resolvePath(b)
	if err != nil {
		return false, err
	}
	return identity.SamePathName(filepath.Clean(resolvedA), filepath.Clean(resolvedB)), nil
}

// resolvePath resolves a path's nearest existing ancestor, the path itself
// when it exists, through finalPath and joins the missing rest to it.
func resolvePath(path string) (string, error) {
	var rest []string
	for current := filepath.Clean(path); ; {
		if _, err := statPath(current); err == nil {
			real, err := finalPath(current)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{real}, rest...)...), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path), nil
		}
		rest = append([]string{filepath.Base(current)}, rest...)
		current = parent
	}
}

// operatorSiblings records each installed operator and the Panel role.
// isolatedPanel comes exclusively from isolatedOperatorProgram.
func operatorSiblings(exe, isolatedPanel string) ([]string, panelRoster) {
	operators := []string{exe}
	panels := panelRoster{reserved: []string{exe}}
	panelName := oaProgramFiles([]string{oaPanelProgramStem})[0]
	var installedPanel string
	for _, name := range oaProgramFiles(oaProgramStems) {
		sibling := filepath.Join(filepath.Dir(exe), name)
		if name != panelName {
			panels.reserved = append(panels.reserved, sibling)
		}
		if _, err := os.Stat(sibling); err == nil {
			sibling = identity.CanonicalProgramPath(sibling)
			if !slices.Contains(operators, sibling) {
				operators = append(operators, sibling)
			}
			if name == panelName {
				installedPanel = sibling
			}
		}
	}
	if isolatedPanel != "" && !slices.Contains(operators, isolatedPanel) {
		operators = append(operators, isolatedPanel)
	}
	if isolatedPanel != "" && reservedIsolatedPanelName(isolatedPanel) {
		isolatedPanel = ""
	}
	ctx := context.Background()
	for _, program := range []string{installedPanel, isolatedPanel} {
		held, err := openPanelImage(ctx, fileid.Self, program, fileid.FollowLocalLinks)
		if err != nil {
			continue
		}
		if image := held.FileID(); panels.contains(image) || panels.excludes(ctx, image) {
			held.Close()
			continue
		}
		panels.images = append(panels.images, held)
	}
	return operators, panels
}

// The isolated executable nomination reserves the old runtime and Presenter
// filename spellings. This input rule can only withhold the Panel role.
func reservedIsolatedPanelName(program string) bool {
	stem := programFileStem(identity.NormalizeSubjectProgram(program))
	for _, reserved := range oaProgramStems {
		if reserved != oaPanelProgramStem && strings.EqualFold(stem, reserved) {
			return true
		}
	}
	return false
}

// composeCredentials needs the runtime's rights: the holder decides through its
// policy. Without a state directory there are neither, and credentials are
// omitted.
func composeCredentials(options runtimeFlags, r *runtimeRights, report func(error)) (*runtimeCredentials, error) {
	if r == nil {
		return nil, nil
	}
	state, err := credentialsState(options)
	if err != nil || state == "" {
		return nil, err
	}
	endpoint, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		return nil, err
	}
	c := &runtimeCredentials{runtimeRights: r, endpoint: endpoint}
	dir := filepath.Join(state, "credentials")
	config := credentials.Config{Namespace: namespace, StatePath: filepath.Join(dir, "holder.json"), MachineScope: credentials.MachineScopeAccount(c.owner), OnError: report}
	if config.Backend, c.release, err = credentials.OpenPlatformStore(dir, namespace, c.owner, report); err != nil {
		report(fmt.Errorf("credentials: no platform store: %w", err))
		config.Backend = nil
	}
	// A composition that fails here serves no holder and holds no store.
	fail := func(err error) (*runtimeCredentials, error) {
		if c.release != nil {
			c.release()
		}
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(err)
	}
	if c.holder, err = credentials.Open(config); err != nil {
		return fail(err)
	}
	if c.host, err = credservice.Listen(endpoint, c.holder, c.decide, nil); err != nil {
		return fail(err)
	}
	c.host.OnError = report
	// Grammar is loaded before credentials composition. Actual served flags
	// are frozen by Rights configure before any receiving endpoint is served.
	if r.policy != nil && r.policy.Definitions != nil && r.policy.Definitions.HasSelectors() {
		c.host.Authorization = credservice.AuthorizationConfig{Admit: c.admitScoped, Quiet: c.checkScoped, Legacy: c.checkLegacy}
	}
	return c, nil
}

func credentialOperation(op credservice.Operation) rwire.ConcreteResource {
	return rwire.ConcreteResource{Permission: rwire.PermissionKey{Action: op.Action, ResourceProfile: op.Profile}, Target: slices.Clone(op.Target)}
}

func credentialBatchAuthorization(v rwire.BatchDecision) credentials.Authorization {
	result := credentials.Authorization{Outcome: credentials.AuthorizationUnavailable, Revision: v.Revision}
	switch v.Outcome {
	case rwire.BatchCheckOutcomeInvalid, rwire.BatchCheckOutcomeForbidden, rwire.BatchCheckOutcomeUnavailable:
		result.RightsOutcome = v.Outcome.String()
	}
	if v.Outcome == rwire.BatchCheckOutcomeConflict {
		result.Outcome = credentials.AuthorizationConflict
		return result
	}
	if v.Outcome != rwire.BatchCheckOutcomeChecked || v.Revision == "" || len(v.Outcomes) != 1 || v.CollectionOutcome != nil {
		return result
	}
	result.RightsOutcome = v.Outcomes[0].String()
	switch {
	case v.Outcomes[0] == rwire.DecisionOutcomePermitted:
		result.Outcome = credentials.Authorized
	case rightsclient.Evaluated(v.Outcomes[0]):
		result.Outcome = credentials.AuthorizationDenied
	}
	return result
}

func (c *runtimeCredentials) admitScoped(ctx context.Context, subject cwire.Subject, op credservice.Operation, _ string) credentials.Authorization {
	if c == nil || c.runtimeRights == nil || subject.Account != c.owner || subject.Program == "" || ctx.Err() != nil {
		return credentials.Authorization{Outcome: credentials.AuthorizationDenied}
	}
	if peer := credservice.PeerFromContext(ctx); peer != nil {
		ctx = inference.WithPeer(ctx, peer)
	}
	return credentialBatchAuthorization(c.runtimeRights.decideTypedAsking(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, credentialOperation(op)))
}

// checkScoped is quiet and may run under Holder's mutex. It uses only the
// shared immutable policy snapshot and captured canonical target.
func (c *runtimeCredentials) checkScoped(ctx context.Context, subject cwire.Subject, op credservice.Operation, expected string) credentials.Authorization {
	if c == nil || c.policy == nil || subject.Account != c.owner || subject.Program == "" || ctx.Err() != nil {
		return credentials.Authorization{Outcome: credentials.AuthorizationUnavailable}
	}
	return credentialBatchAuthorization(c.runtimeRights.CheckBatchContext(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, rwire.CheckBatchRequest{Resources: []rwire.ConcreteResource{credentialOperation(op)}, ExpectedRevision: expected}))
}

func (c *runtimeCredentials) checkLegacy(ctx context.Context, subject cwire.Subject, action, resource string, op *credservice.Operation) credentials.Authorization {
	if c == nil || c.policy == nil || subject.Account != c.owner || subject.Program == "" || ctx.Err() != nil {
		return credentials.Authorization{Outcome: credentials.AuthorizationUnavailable}
	}
	request := rwire.LegacyOperationRequest{LegacyAction: action, LegacyResource: resource}
	if op != nil {
		concrete := credentialOperation(*op)
		request.ConcreteOperation = &concrete
	}
	v := c.runtimeRights.CheckLegacyOperationContext(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, request)
	result := credentials.Authorization{Outcome: credentials.AuthorizationUnavailable, Revision: v.PolicyRevision, RightsOutcome: v.Outcome.String()}
	if v.PolicyRevision == "" {
		return result
	}
	switch {
	case v.Outcome == rwire.DecisionOutcomePermitted:
		result.Outcome = credentials.Authorized
	case rightsclient.Evaluated(v.Outcome):
		result.Outcome = credentials.AuthorizationDenied
	}
	return result
}

func credentialUseOperation(name string) (credservice.Operation, bool) {
	target, err := credentials.PrepareCredentialTarget(name)
	return credservice.Operation{Action: credentials.ActionApply, Profile: credentials.ScopedUseResourceProfile, Target: target}, err == nil
}

// legacyUseAuthorization is the original route's use decision and its quiet
// policy fence. quiet decides the use asking nobody (decideQuietly), as
// download execution does (INF-A6); otherwise a not_granted use admits its
// first-use question (decide).
func (c *runtimeCredentials) legacyUseAuthorization(ctx context.Context, subject cwire.Subject, name string, quiet bool) (credentials.Decider, credentials.AuthorizationGuard, *credentials.Authorization) {
	op, valid := credentialUseOperation(name)
	decided := false
	evidence := &credentials.Authorization{}
	base := c.decide
	if quiet {
		base = c.decideQuietly
	}
	decide := func(ctx context.Context, subject cwire.Subject, action, resource string) (string, error) {
		word, err := base(ctx, subject, action, resource)
		decided = true
		return word, err
	}
	guard := func() credentials.AuthorizationOutcome {
		*evidence = credentials.Authorization{}
		if !valid || ctx.Err() != nil {
			return credentials.AuthorizationUnavailable
		}
		// Legacy Apply keeps eligibility then outside-lock admission. The quiet
		// policy fence runs after Decide and again after the backend wait.
		if !decided {
			return credentials.Authorized
		}
		v := c.checkLegacy(ctx, subject, credentials.ActionApply, credentials.ResourceFor(name), &op)
		if ctx.Err() != nil {
			return credentials.AuthorizationUnavailable
		}
		*evidence = v
		return v.Outcome
	}
	return decide, guard, evidence
}

// scopedUse is the scoped route's use authorization. quiet admits the use
// with the quiet check, asking nobody, as download execution does (INF-A6);
// otherwise a not_granted use admits its first-use question (admitScoped).
func (c *runtimeCredentials) scopedUse(ctx context.Context, subject cwire.Subject, name string, quiet bool) credentials.ScopedAuthorization {
	op, valid := credentialUseOperation(name)
	if !valid {
		return credentials.ScopedAuthorization{}
	}
	admit := func() credentials.Authorization { return c.admitScoped(ctx, subject, op, "") }
	if quiet {
		admit = func() credentials.Authorization { return c.checkScoped(ctx, subject, op, "") }
	}
	return credentials.ScopedAuthorization{
		Admit: admit,
		Check: func(expected string) credentials.Authorization { return c.checkScoped(ctx, subject, op, expected) },
	}
}

// ScopedCredentialsAvailable advertises the explicit Download route only after
// all six receiving definitions and the complete compatibility fence exist.
func (c *runtimeCredentials) ScopedCredentialsAvailable() bool {
	if c == nil || c.host == nil || !c.host.ScopedAvailable() || c.policy == nil || c.policy.Definitions == nil {
		return false
	}
	for _, member := range []struct{ action, profile string }{
		{credentials.ActionSecretRead, credentials.NamedSecretResourceProfile},
		{credentials.ActionSecretCreate, credentials.NamedSecretResourceProfile},
		{credentials.ActionSecretRotate, credentials.NamedSecretResourceProfile},
		{credentials.ActionSecretRevoke, credentials.NamedSecretResourceProfile},
		{credentials.ActionSecretMetadataRead, credentials.NamedSecretResourceProfile},
		{credentials.ActionApply, credentials.ScopedUseResourceProfile},
	} {
		definition, ok := c.policy.Definitions.SelectorDefinition(rwire.PermissionKey{Action: member.action, ResourceProfile: member.profile})
		if !ok || !definition.Definition.Served {
			return false
		}
	}
	return true
}

// decideQuietly is decide for OpenAbstractions' own reads of a key, such as
// the router's model listing and download execution's credential applier
// (DL-K4): the same decision, and no first-use question.
func (c *runtimeCredentials) decideQuietly(ctx context.Context, subject cwire.Subject, action, resource string) (string, error) {
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	if _, incoming := ctx.Value(incomingRemoteKey{}).(incomingRemoteCall); incoming || remote.ValidSubjectProgram(bound.Program) {
		return c.decide(ctx, subject, action, resource)
	}
	if action == credentials.ActionApply {
		return c.useDecision(ctx, bound, resource).String(), nil
	}
	return c.runtimeRights.decide(ctx, bound, action, resource).Outcome.String(), nil
}

// decide asks the in-process decision policy about one subject.
func (c *runtimeCredentials) decide(ctx context.Context, subject cwire.Subject, action, resource string) (string, error) {
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	call, incoming := ctx.Value(incomingRemoteKey{}).(incomingRemoteCall)
	if incoming || remote.ValidSubjectProgram(bound.Program) {
		if !incoming || c == nil || c.runtimeRights == nil || !call.matchesSubject(c.owner, bound.Account, bound.Program) {
			return rwire.DecisionOutcomeUnavailable.String(), nil
		}
		if action == credentials.ActionApply {
			return c.useDecision(ctx, bound, resource).String(), nil
		}
		return c.runtimeRights.decideRemote(ctx, call.peer, call.admit, action, resource, call.minimum).Outcome.String(), nil
	}
	if action == credentials.ActionApply {
		outcome := c.useDecision(ctx, bound, resource)
		if outcome == rwire.DecisionOutcomeNotGranted && c.firstUse != nil {
			// The gateway window holds the refused call open on this question
			// (INF-W10).
			inference.AskedFrom(ctx).Add(c.firstUse(bound, requesterPublisher(boundPeer(ctx, bound)), action, resource))
		}
		return outcome.String(), nil
	}
	return c.runtimeRights.decide(ctx, bound, action, resource).Outcome.String(), nil
}

// useDecision decides abstraction.credentials/apply on resource for bound,
// the legacy rule and the typed use operation together, asking nobody. Apply
// decides use by it, and a running inference operation decides again by it
// (inference CONTRACT.md INF-A6).
func (c *runtimeCredentials) useDecision(ctx context.Context, bound rwire.Subject, resource string) rwire.DecisionOutcome {
	request := rwire.LegacyOperationRequest{LegacyAction: credentials.ActionApply, LegacyResource: resource}
	if target, ok := credentials.ParseResource(resource); ok {
		if op, valid := credentialUseOperation(target.Name); valid {
			concrete := credentialOperation(op)
			request.ConcreteOperation = &concrete
		}
	}
	call, incoming := ctx.Value(incomingRemoteKey{}).(incomingRemoteCall)
	if incoming || remote.ValidSubjectProgram(bound.Program) {
		if !incoming || c == nil || c.runtimeRights == nil || !call.matchesSubject(c.owner, bound.Account, bound.Program) || ctx.Err() != nil {
			return rwire.DecisionOutcomeUnavailable
		}
		mapped, err := remote.SubjectOf(call.peer, call.admit)
		if err != nil || mapped.Program() != bound.Program {
			return rwire.DecisionOutcomeForbidden
		}
		if request.ConcreteOperation == nil {
			// Unknown typed targets cannot take the legacy-only path.
			return rwire.DecisionOutcomeUnavailable
		}
		if c.policy == nil {
			return rwire.DecisionOutcomeUnavailable
		}
		ctx, cancel := budget.Within(ctx, decisionBudget, decisionBudget)
		defer cancel()
		return c.policy.CheckLegacyOperationAtLeastRevisionContext(ctx, bound, request, call.minimum).Decision.Outcome
	}
	return c.runtimeRights.CheckLegacyOperationContext(ctx, bound, request).Outcome
}

// configure adds the credentials host and the in-process model applier to the
// runtime's options, and registers the credentials actions.
func (c *runtimeCredentials) configure(o *host.Options) {
	if c == nil {
		return
	}
	o.Credentials, o.CredentialsEndpoint = c.host, c.endpoint
	o.RightsActions = append(o.RightsActions, cwire.ResourceActions...)
	if o.ModelRegistry != nil {
		o.ModelCredentials = c.applyForModel
	}
}

// close releases a composed host the runtime never took over, and the
// platform store once the runtime stops serving.
func (c *runtimeCredentials) close() {
	if c != nil {
		//unchecked: close has no return value to report a close failure through
		c.host.Close()
		if c.release != nil {
			c.release()
		}
	}
}

// executor gives the configured download execution profile this applier.
func (c *runtimeCredentials) executor(e acceptanceprovider.Executor) acceptanceprovider.Executor {
	if c == nil {
		return e
	}
	switch v := e.(type) {
	case downloadserve.HTTPExecution:
		v.Credentials = c
		return v
	case downloadserve.LegacySinkExecution:
		v.Credentials = c
		return v
	}
	return e
}

// apply applies a credential for one request of subject. quiet decides the
// use asking nobody: download execution runs after admission asked, and a
// rule removed since then fails the job with no question (INF-A6, RIGHTS-FLOW
// G3).
func (c *runtimeCredentials) apply(ctx context.Context, subject cwire.Subject, consumer, name, host string, quiet bool) (map[string]string, error) {
	decide, guard, evidence := c.legacyUseAuthorization(ctx, subject, name, quiet)
	result := c.holder.ApplyAuthorized(ctx, cwire.Use{Subject: subject, Consumer: consumer, Name: name, Target: host}, decide, guard, evidence)
	if result.Outcome != cwire.ApplyOutcomeApplied {
		return nil, download.CredentialRefusal(name, result.Outcome.String())
	}
	return result.Headers, nil
}

func (c *runtimeCredentials) applyForModel(ctx context.Context, account, program, name, host string) (map[string]string, error) {
	return c.apply(ctx, cwire.Subject{Account: account, Program: program}, modelCredentialConsumer, name, host, false)
}

// ApplyCredential serves download execution. The job service records an opaque
// caller scope, a digest of the caller's account and program; the subject is
// the program whose exact apply rule on this credential yields that scope. A
// scope no rule names can never be permitted, and reads not_permitted.
func (c *runtimeCredentials) ApplyCredential(ctx context.Context, scope, name, target string) (map[string]string, error) {
	subject, err := c.scopeSubject(scope, name)
	if err != nil {
		return nil, err
	}
	return c.apply(ctx, subject, downloadserve.CredentialConsumer, name, target, true)
}

func (c *runtimeCredentials) ApplySubjectCredential(ctx context.Context, subject acceptanceprovider.AuthenticatedSubject, name, target string) (map[string]string, error) {
	if subject.Account != c.owner || subject.Program == "" {
		return nil, download.CredentialRefusal(name, "not_permitted")
	}
	return c.apply(ctx, cwire.Subject{Account: subject.Account, Program: subject.Program}, downloadserve.CredentialConsumer, name, target, true)
}

// CheckSubjectCredential serves bound download admission [JOB-A16]. The
// decision admits the credential's first-use question on not_granted; a
// not_permitted refusal carries each question it raised or joined that is
// still pending, which admission names (INF-W11, RIGHTS-FLOW ruling 2).
func (c *runtimeCredentials) CheckSubjectCredential(ctx context.Context, subject acceptanceprovider.AuthenticatedSubject, name, target string) error {
	if subject.Account != c.owner || subject.Program == "" {
		return download.CredentialRefusal(name, "not_permitted")
	}
	bound := cwire.Subject{Account: subject.Account, Program: subject.Program}
	asked := &inference.Asked{}
	ctx = inference.WithAsked(ctx, asked)
	decide, guard, evidence := c.legacyUseAuthorization(ctx, bound, name, false)
	result := c.holder.CheckAuthorized(ctx, cwire.Use{Subject: bound, Consumer: downloadserve.CredentialConsumer, Name: name, Target: target}, decide, guard, evidence)
	return c.checkRefusal(bound, name, result.Outcome, asked)
}

// checkRefusal is the error of a download admission check: nil once applied,
// and a not_permitted refusal naming its pending questions.
func (c *runtimeCredentials) checkRefusal(bound cwire.Subject, name string, outcome cwire.ApplyOutcome, asked *inference.Asked) error {
	if outcome == cwire.ApplyOutcomeApplied {
		return nil
	}
	refusal := download.CredentialRefusal(name, outcome.String())
	if outcome != cwire.ApplyOutcomeNotPermitted {
		return refusal
	}
	return c.runtimeRights.refusalNamingQuestions(refusal, rwire.Subject{Account: bound.Account, Program: bound.Program}, asked)
}

// CheckCredential serves download admission: the holder's Check for the
// subject the scope maps to, which reads no secret [JOB-A16].
func (c *runtimeCredentials) CheckCredential(ctx context.Context, scope, name, target string) error {
	subject, err := c.scopeSubject(scope, name)
	if err != nil {
		return err
	}
	decide, guard, evidence := c.legacyUseAuthorization(ctx, subject, name, false)
	result := c.holder.CheckAuthorized(ctx, cwire.Use{Subject: subject, Consumer: downloadserve.CredentialConsumer, Name: name, Target: target}, decide, guard, evidence)
	if result.Outcome != cwire.ApplyOutcomeApplied {
		return download.CredentialRefusal(name, result.Outcome.String())
	}
	return nil
}

func (c *runtimeCredentials) ApplyScopedSubjectCredential(ctx context.Context, subject acceptanceprovider.AuthenticatedSubject, name, target string) (map[string]string, error) {
	if !c.ScopedCredentialsAvailable() {
		return nil, download.CredentialRefusal(name, "unavailable")
	}
	if subject.Account != c.owner || subject.Program == "" {
		return nil, download.CredentialRefusal(name, "not_permitted")
	}
	bound := cwire.Subject{Account: subject.Account, Program: subject.Program}
	// Execution asks nobody (INF-A6); admission asked.
	result := c.holder.ApplyScoped(ctx, cwire.Use{Subject: bound, Consumer: downloadserve.CredentialConsumer, Name: name, Target: target}, c.scopedUse(ctx, bound, name, true))
	if result.Outcome != cwire.ApplyOutcomeApplied {
		return nil, download.CredentialRefusal(name, result.Outcome.String())
	}
	return result.Headers, nil
}

func (c *runtimeCredentials) CheckScopedSubjectCredential(ctx context.Context, subject acceptanceprovider.AuthenticatedSubject, name, target string) error {
	if !c.ScopedCredentialsAvailable() {
		return download.CredentialRefusal(name, "unavailable")
	}
	if subject.Account != c.owner || subject.Program == "" {
		return download.CredentialRefusal(name, "not_permitted")
	}
	bound := cwire.Subject{Account: subject.Account, Program: subject.Program}
	// The typed decision admits the use's first-use question on not_granted
	// (decideTypedAsking); the refusal names it as CheckSubjectCredential's.
	asked := &inference.Asked{}
	ctx = inference.WithAsked(ctx, asked)
	result := c.holder.CheckScoped(ctx, cwire.Use{Subject: bound, Consumer: downloadserve.CredentialConsumer, Name: name, Target: target}, c.scopedUse(ctx, bound, name, false))
	return c.checkRefusal(bound, name, result.Outcome, asked)
}

// scopeSubject inverts a job caller scope through the exact apply rules on the
// credential. A scope no rule names reads not_permitted.
func (c *runtimeCredentials) scopeSubject(scope, name string) (cwire.Subject, error) {
	snapshot, err := c.policy.OperatorSnapshot()
	if err != nil {
		return cwire.Subject{}, download.CredentialRefusal(name, "unavailable")
	}
	for _, rule := range snapshot.Rules {
		if rule.Action != credentials.ActionApply || rule.Resource != credentials.ResourceFor(name) || rule.Subject.Account != c.owner {
			continue
		}
		candidate, err := host.OwnerProgramScope(c.kind, c.owner, rule.Subject.Program)
		if err == nil && candidate == scope {
			return cwire.Subject{Account: c.owner, Program: rule.Subject.Program}, nil
		}
	}
	return cwire.Subject{}, download.CredentialRefusal(name, "not_permitted")
}

// localScopeSubject recovers a verified caller scope without using a
// credential rule. A current policy subject is only an identity candidate;
// inference still asks the complete action for the selected host before any
// content access or execution, so the opaque scope grants no authority.
func (c *runtimeCredentials) localScopeSubject(scope string) (cwire.Subject, error) {
	snapshot, err := c.policy.OperatorSnapshot()
	if err != nil {
		return cwire.Subject{}, download.CredentialRefusal("", "unavailable")
	}
	seen := map[string]bool{}
	for _, rule := range snapshot.Rules {
		if rule.Subject.Account != c.owner || rule.Subject.Program == "" || seen[rule.Subject.Program] {
			continue
		}
		seen[rule.Subject.Program] = true
		candidate, err := host.OwnerProgramScope(c.kind, c.owner, rule.Subject.Program)
		if err == nil && candidate == scope {
			return cwire.Subject{Account: c.owner, Program: rule.Subject.Program}, nil
		}
	}
	return cwire.Subject{}, download.CredentialRefusal("", "not_permitted")
}
