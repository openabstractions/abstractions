package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
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
}

// credentialsBackendFile names the store an operator chose for this runtime's
// state directory. Only Linux reads it: file-0600 selects the opt-in file store.
const credentialsBackendFile = "backend"

// serviceAccounts are the Windows well-known service principals. A runtime
// running as one of them, or as root elsewhere, is machine scope.
var serviceAccounts = []string{"S-1-5-18", "S-1-5-19", "S-1-5-20"}

func machineScope(account string) bool {
	return slices.Contains(serviceAccounts, account) || account == "0"
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
		if credentialsEndpoint, err = bootstrap.Endpoint(options.isolated + "-credentials"); err != nil {
			return "", "", "", err
		}
		rightsEndpoint, err = bootstrap.Endpoint(options.isolated + "-rights")
		return credentialsEndpoint, rightsEndpoint, "oa-isolated-" + options.isolated, err
	case options.endpoint != "":
		sum := sha256.Sum256([]byte(options.endpoint))
		return options.endpoint + "-credentials", options.endpoint + "-rights", "oa-runtime-" + hex.EncodeToString(sum[:6]), nil
	}
	if credentialsEndpoint, err = bootstrap.Endpoint("credentials-v1"); err != nil {
		return "", "", "", err
	}
	rightsEndpoint, err = bootstrap.Endpoint("rights-authorization-v1")
	return credentialsEndpoint, rightsEndpoint, credentials.DefaultNamespace, err
}

// platformBackend selects the account's platform store. Linux uses Secret
// Service unless the operator chose the file store for this state directory; an
// unreachable Secret Service stays configured and reads unavailable. A macOS
// runtime without a keychain access group entitlement keeps the keychain
// configured and reads unavailable the same way.
func platformBackend(dir string) (credentials.Backend, error) {
	switch runtime.GOOS {
	case "windows":
		return credentials.NewCredentialManager()
	case "darwin":
		backend, err := credentials.NewKeychain()
		if errors.Is(err, credentials.ErrUnavailable) {
			return credentials.Unreachable(credentials.StoreMacOSKeychain, credentials.MaxSecretBytes, err), nil
		}
		return backend, err
	case "linux":
		chosen, err := os.ReadFile(filepath.Join(dir, credentialsBackendFile))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if strings.TrimSpace(string(chosen)) == credentials.StoreFile {
			return credentials.NewFileBackend(filepath.Join(dir, "items"))
		}
		backend, err := credentials.NewSecretService("")
		if err != nil {
			return credentials.Unreachable(credentials.StoreSecretService, credentials.MaxSecretBytes, err), nil
		}
		return backend, nil
	}
	return nil, credentials.ErrUnsupportedPlatform
}

// operatorPrograms are the runtime's own executable and its installed operator
// siblings: the command line, its windowless link and the Abstraction Panel,
// the programs holding holder.manage, holder.read and rights operator authority
// by installation (feedback/credentials-holder-design.md, operator tools).
func operatorPrograms() ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return operatorSiblings(filepath.Clean(exe)), nil
}

// operatorSiblings names exe and each operator program installed beside it.
func operatorSiblings(exe string) []string {
	programs := []string{exe}
	for _, name := range []string{"openabstractions", "openabstractionsw", "Abstraction Panel"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		sibling := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(sibling); err == nil && !slices.Contains(programs, sibling) {
			programs = append(programs, sibling)
		}
	}
	return programs
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	config := credentials.Config{Namespace: namespace, StatePath: filepath.Join(dir, "holder.json"), MachineScope: machineScope(c.owner), OnError: report}
	if !config.MachineScope {
		if config.Backend, err = platformBackend(dir); err != nil {
			report(fmt.Errorf("credentials: no platform store: %w", err))
			config.Backend = nil
		}
	}
	if c.holder, err = credentials.Open(config); err != nil {
		return nil, err
	}
	if c.host, err = credservice.Listen(endpoint, c.holder, c.decide, nil); err != nil {
		return nil, err
	}
	c.host.OnError = report
	return c, nil
}

// decide asks the in-process decision policy about one subject.
func (c *runtimeCredentials) decide(ctx context.Context, subject cwire.Subject, action, resource string) (string, error) {
	decision := c.runtimeRights.decide(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, action, resource)
	return decision.Outcome.String(), nil
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

// close releases a composed host the runtime never took over.
func (c *runtimeCredentials) close() {
	if c != nil {
		c.host.Close()
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

func (c *runtimeCredentials) apply(ctx context.Context, subject cwire.Subject, consumer, name, host string) (map[string]string, error) {
	result := c.holder.Apply(ctx, cwire.Use{Subject: subject, Consumer: consumer, Name: name, Target: host}, c.decide)
	if result.Outcome != cwire.ApplyOutcomeApplied {
		return nil, download.CredentialRefusal(name, result.Outcome.String())
	}
	return result.Headers, nil
}

func (c *runtimeCredentials) applyForModel(ctx context.Context, account, program, name, host string) (map[string]string, error) {
	return c.apply(ctx, cwire.Subject{Account: account, Program: program}, modelCredentialConsumer, name, host)
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
	return c.apply(ctx, subject, downloadserve.CredentialConsumer, name, target)
}

// CheckCredential serves download admission: the holder's Check for the
// subject the scope maps to, which reads no secret [JOB-A16].
func (c *runtimeCredentials) CheckCredential(ctx context.Context, scope, name, target string) error {
	subject, err := c.scopeSubject(scope, name)
	if err != nil {
		return err
	}
	result := c.holder.Check(ctx, cwire.Use{Subject: subject, Consumer: downloadserve.CredentialConsumer, Name: name, Target: target}, c.decide)
	if result.Outcome != cwire.ApplyOutcomeApplied {
		return download.CredentialRefusal(name, result.Outcome.String())
	}
	return nil
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
