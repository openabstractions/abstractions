package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

// Files the inference operator keeps beside hosts.json in <state>/inference.
const (
	inferenceJournalFile = "audit.jsonl"
	inferenceKeysFile    = "keys.json"
	// localKeyPrefix begins every local key; the name tag follows it.
	localKeyPrefix = "oalk_"
	// hostAddWhy is the provenance of the rules AddHost writes.
	hostAddWhy         = "inference server add"
	credentialStoreWhy = "credential stored for declared inference server"
	// hostsSurveyAge re-reads the hosts for a listing older than this.
	hostsSurveyAge = 30 * time.Second
	// declaredByOperator is the declared_by of a host added through operator@1.
	declaredByOperator = "operator"
)

// defaultProfiles is what a host serves when its entry names no profiles:
// every seeded profile on the OpenAI-compatible wire, which the local kinds
// speak, and, for any other hosted wire, router.DefaultProfiles' table for
// it (openai-realtime serves live, deepgram-prerecorded serves
// transcription, and so on), read from the same *router.Host construction
// the router itself routes through, never copied here.
func defaultProfiles(hosted bool, wire string) []string {
	if !hosted || wire == router.WireOpenAICompatible {
		return slices.Clone(iwire.HostProfiles)
	}
	return router.DefaultProfiles(router.NewHosted("", "", wire, ""))
}

func validProfiles(profiles []string) bool {
	if len(profiles) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range profiles {
		if seen[p] || !slices.Contains(iwire.HostProfiles, p) && !ownedWireKind.MatchString(p) {
			return false
		}
		seen[p] = true
	}
	return true
}

func ceilingOf(c *iwire.CeilingLimit) inference.Ceiling {
	return inference.Ceiling{TokensPerDay: c.TokensPerDay, MicrosPerDay: c.MicrosPerDay, RequestsPerDay: c.RequestsPerDay,
		ImagesPerDay: c.ImagesPerDay, AudioSecondsPerDay: c.AudioSecondsPerDay, CharactersPerDay: c.CharactersPerDay}
}

func ceilingLimit(c inference.Ceiling) *iwire.CeilingLimit {
	return &iwire.CeilingLimit{TokensPerDay: c.TokensPerDay, MicrosPerDay: c.MicrosPerDay, RequestsPerDay: c.RequestsPerDay,
		ImagesPerDay: c.ImagesPerDay, AudioSecondsPerDay: c.AudioSecondsPerDay, CharactersPerDay: c.CharactersPerDay}
}

// keyRecord is one local key's metadata in keys.json. The key itself lives
// only in the platform store, as a holder record of kind local-key@1.
type keyRecord struct {
	Program      string `json:"program"`
	Name         string `json:"name"`
	Credential   string `json:"credential,omitempty"`
	IssuedUnixMS int64  `json:"issued_unix_ms"`
	IssuedBy     string `json:"issued_by"`
}

type keyIndex struct {
	Version int         `json:"version"`
	Keys    []keyRecord `json:"keys"`
}

func (k *keyIndex) byName(name string) (keyRecord, bool) {
	for _, r := range k.Keys {
		if r.Name == name {
			return r, true
		}
	}
	return keyRecord{}, false
}

// samePrograms compares executable paths the way the platform names files.
func samePrograms(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

var (
	inferenceHostName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	credentialName    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	ownedWireKind     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}/[a-z0-9][a-z0-9_.-]{0,62}@[1-9][0-9]{0,5}$`)
	localKeyShape     = regexp.MustCompile(`^oalk_([0-9a-f]{16})([0-9a-f]{8})_[A-Za-z0-9_-]{43}$`)
	// hostedWireKinds is every router.thrift wire_kinds member a hosted host
	// may declare, excluding oa-remote@1: a remote runtime is its own
	// declaration role, validated by validProviderDeclaration's d.remote()
	// branch, never by validEntry. router.thrift FAC-R6's HostEntry.profiles
	// doc already names "a wire kind (router wire_kinds or <owner>/<name>@<n>)"
	// as the accepted shape; this was openai-compatible and anthropic-messages
	// only, which left every other wire_kinds member, including
	// openai-realtime, refused here as "invalid host" with no caller-visible
	// reason beyond the runtime's own log.
	hostedWireKinds = func() []string {
		out := make([]string, 0, len(routerwire.WireKinds))
		for _, k := range routerwire.WireKinds {
			if k != router.WireRemote {
				out = append(out, k)
			}
		}
		return out
	}()
)

// localKeyName is the holder record name a presented key names.
func localKeyName(key string) (string, bool) {
	m := localKeyShape.FindStringSubmatch(key)
	if m == nil {
		return "", false
	}
	return "local-key." + m[1] + "." + m[2], true
}

// mintLocalKey makes a key for program: a tag of the program, a random suffix
// that keeps a reissued key's record name distinct from a revoked one's
// tombstone, and 32 random bytes.
func mintLocalKey(program string) (name, key string, err error) {
	normalized := program
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(program)
	}
	sum := sha256.Sum256([]byte(normalized))
	tag := hex.EncodeToString(sum[:8])
	var suffix [4]byte
	var secret [32]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", err
	}
	s := hex.EncodeToString(suffix[:])
	return "local-key." + tag + "." + s, localKeyPrefix + tag + s + "_" + base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

// inferenceOperator serves abstraction.inference/operator@1 for the runtime:
// the host configuration in hosts.json, the local keys in keys.json and the
// holder, and the audit journal. Every call decides its action on resource
// account for the bound caller through the runtime's rights policy.
type inferenceOperator struct {
	mu          sync.Mutex
	dir         string
	credentials *runtimeCredentials
	router      *router.Router
	ceilings    *inference.Ceilings
	journal     *inference.Journal
	keys        keyIndex
	report      func(error)
	// gateway is the window control SetGateway applies; assigned at composition.
	gateway *gatewayControl
	// providers holds the provider declarations (provider.go); nil when they
	// could not be opened.
	providers *runtimeProviders
}

func newInferenceOperator(dir string, c *runtimeCredentials, r *router.Router, ceilings *inference.Ceilings, journal *inference.Journal, report func(error)) (*inferenceOperator, error) {
	o := &inferenceOperator{dir: dir, credentials: c, router: r, ceilings: ceilings, journal: journal, report: report, keys: keyIndex{Version: 1}}
	raw, err := os.ReadFile(filepath.Join(dir, inferenceKeysFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &o.keys); err != nil || o.keys.Version != 1 {
			return nil, fmt.Errorf("inference: %s is not a version 1 key index", inferenceKeysFile)
		}
	}
	return o, nil
}

// gate decides action on account for caller: "" when permitted, else the
// operator outcome word.
func (o *inferenceOperator) gate(ctx context.Context, caller inference.Subject, action string) string {
	if caller.Account == "" || caller.Program == "" || caller.Account != o.credentials.owner {
		return "forbidden"
	}
	decision := o.credentials.runtimeRights.decide(ctx, rwire.Subject{Account: caller.Account, Program: caller.Program}, action, credentials.ResourceAccount)
	switch decision.Outcome {
	case rwire.DecisionOutcomePermitted:
		return ""
	case rwire.DecisionOutcomeDenied, rwire.DecisionOutcomeNotGranted, rwire.DecisionOutcomeUnknownAction:
		return "forbidden"
	}
	return "unavailable"
}

func writeAtomically(path string, value any) error {
	return cas.Change(path, func([]byte) ([]byte, error) {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(raw, '\n'), nil
	})
}

// readConfig reads hosts.json and its revision, the digest of its bytes.
func (o *inferenceOperator) readConfig() (inferenceHosts, string, error) {
	raw, err := os.ReadFile(filepath.Join(o.dir, inferenceHostsFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return inferenceHosts{}, "", err
	}
	sum := sha256.Sum256(raw)
	revision := "hosts-v1:" + hex.EncodeToString(sum[:12])
	var config inferenceHosts
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			return config, revision, err
		}
	}
	return config, revision, nil
}

// hostFiles is every host declaration the registry holds, in name order.
func (o *inferenceOperator) hostFiles() []providerFile {
	if o.providers == nil {
		return nil
	}
	return o.providers.hostFiles()
}

func loopbackName(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// validEntry returns the invalid field of an AddHost entry, or "".
func validEntry(e iwire.HostEntry) string {
	if !inferenceHostName.MatchString(e.Name) {
		return "name"
	}
	if e.DeclaredBy != "" {
		return "declared_by"
	}
	if !validProfiles(e.Profiles) {
		return "profiles"
	}
	u, err := url.Parse(e.Base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackName(u.Hostname()))) {
		return "base"
	}
	if !e.Hosted {
		switch {
		case !slices.Contains(iwire.LocalHostKinds, e.Kind):
			return "kind"
		case e.Name != e.Kind:
			return "name"
		case e.Credential != "" || e.Ceiling != nil:
			return "credential"
		}
		return ""
	}
	if !slices.Contains(hostedWireKinds, e.Kind) && !ownedWireKind.MatchString(e.Kind) {
		return "kind"
	}
	if e.Credential != "" && !credentialName.MatchString(e.Credential) {
		return "credential"
	}
	if c := e.Ceiling; c != nil && (ceilingOf(c).Negative() || e.Credential == "") {
		return "ceiling"
	}
	return ""
}

// refreshHosts gives the router the declarations again, after one changed.
func (o *inferenceOperator) refreshHosts() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.ceilings.ReplaceLimits(declarationBudgets(o.hostFiles())); err != nil {
		o.report(err)
		return
	}
	hosts := routerHosts(o.providers)
	o.router.SetHosts(hosts...)
	if len(hosts) > 0 {
		o.router.Survey()
	}
}

// hostRevision is the revision host edits take, the registry's revision.
func (o *inferenceOperator) hostRevision() (string, error) {
	if o.providers == nil {
		return "", errors.New("inference: the runtime holds no declarations")
	}
	o.providers.mu.Lock()
	defer o.providers.mu.Unlock()
	_, revision, err := o.providers.read()
	return revision, err
}

func (o *inferenceOperator) Hosts(ctx context.Context, caller inference.Subject) iwire.HostList {
	refused := func(outcome iwire.ListOutcome) iwire.HostList {
		return iwire.HostList{Outcome: outcome, Hosts: []iwire.HostState{}}
	}
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		return refused(inferenceListRefusal(word))
	}
	o.mu.Lock()
	_, _, err := o.readConfig()
	revision, revisionErr := o.hostRevision()
	o.mu.Unlock()
	if err != nil || revisionErr != nil {
		o.report(errors.Join(err, revisionErr))
		return refused(iwire.ListOutcomeUnavailable)
	}
	_, _, _, _, at := o.router.Residency(false)
	states, _, _, _, _ := o.router.Residency(time.Since(at) > hostsSurveyAge)
	byName := map[string]router.HostState{}
	for _, s := range states {
		byName[s.Host] = s
	}
	list := iwire.HostList{Outcome: iwire.ListOutcomePage, Revision: revision, Hosts: []iwire.HostState{}}
	for _, f := range o.hostFiles() {
		if f.Disabled {
			continue
		}
		entry := f.hostEntry()
		s, surveyed := byName[entry.Name]
		state := iwire.HostState{Entry: entry, Up: s.Up, Why: hostedDownReason(s.Why, entry.Credential, o.credentials.operators[0])}
		if !surveyed {
			state.Why = "not surveyed yet"
		}
		if entry.Credential != "" {
			day, tokens, micros, requests, images, audioSeconds, characters := o.ceilings.SpendUnits(entry.Credential)
			state.Spend = &iwire.Spend{Day: day, Tokens: tokens, Micros: micros, Requests: requests, Images: images, AudioSeconds: audioSeconds, Characters: characters}
		}
		list.Hosts = append(list.Hosts, state)
	}
	return list
}

func (o *inferenceOperator) AddHost(ctx context.Context, caller inference.Subject, expected string, entry iwire.HostEntry) iwire.HostChange {
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		return iwire.HostChange{Outcome: inferenceEditRefusal(word)}
	}
	if field := validEntry(entry); field != "" {
		return iwire.HostChange{Outcome: iwire.EditOutcomeInvalid, Reason: field}
	}
	if o.providers == nil {
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	if len(entry.Profiles) == 0 {
		entry.Profiles = defaultProfiles(entry.Hosted, entry.Kind)
	}
	// A host is a registry declaration of role host: the registry decides the
	// revision, the name and the count, and its watchers give the router the
	// new host. The registry's own lock is taken without o.mu.
	applied := o.providers.add(expected, newHostFile(entry, declaredByOperator))
	if applied.Outcome != wire.DeclarationEditOutcomeApplied {
		return iwire.HostChange{Outcome: hostEditOf(applied.Outcome), Revision: applied.Revision, Reason: applied.Reason}
	}
	o.refreshHosts()
	change := iwire.HostChange{Outcome: iwire.EditOutcomeApplied, Revision: applied.Revision}
	if err := o.writeHostRules(caller, entry); err != nil {
		o.report(err)
		change.Reason = "rules:" + err.Error()
	}
	return change
}

// hostEditOf is the host edit outcome of a registry edit outcome.
func hostEditOf(outcome wire.DeclarationEditOutcome) iwire.EditOutcome {
	switch outcome {
	case wire.DeclarationEditOutcomeApplied:
		return iwire.EditOutcomeApplied
	case wire.DeclarationEditOutcomeConflict:
		return iwire.EditOutcomeConflict
	case wire.DeclarationEditOutcomeUnknown:
		return iwire.EditOutcomeUnknown
	case wire.DeclarationEditOutcomeInvalid:
		return iwire.EditOutcomeInvalid
	case wire.DeclarationEditOutcomeForbidden:
		return iwire.EditOutcomeForbidden
	}
	return iwire.EditOutcomeUnavailable
}

// writeHostRules writes complete on the host for the runtime's operator
// programs and the caller, and, for a hosted host with a credential, the
// runtime's own apply rule its listing reads need. An existing rule on a
// target, a deny included, is left as it is.
func (o *inferenceOperator) writeHostRules(caller inference.Subject, entry iwire.HostEntry) error {
	r := o.credentials.runtimeRights
	by := rwire.Subject{Account: caller.Account, Program: caller.Program}
	programs := slices.Clone(r.operators)
	if !slices.ContainsFunc(programs, func(p string) bool { return samePrograms(p, caller.Program) }) {
		programs = append(programs, caller.Program)
	}
	var errs []error
	for _, program := range programs {
		errs = append(errs, o.permitRule(by, program, inference.ActionComplete, inference.ResourceHost(entry.Name)))
	}
	errs = append(errs, o.ensureHostedApplyRule(by, entry, hostAddWhy))
	return errors.Join(errs...)
}

func (o *inferenceOperator) permitRule(by rwire.Subject, program, action, resource string) error {
	return o.permitRuleWhy(by, program, action, resource, hostAddWhy)
}

// ensureHostedApplyRule gives the runtime's own operator program the
// abstraction.credentials/apply rule its own survey of a hosted host needs,
// unless a rule on that target already exists: composeInference's
// UseCredentials applies every hosted host's credential as the runtime's own
// program, never the caller's, so this is the one rule that program needs to
// survey the host at all. writeHostRules calls this when AddHost declares
// the host after its credential is registered; credentialStored handles the
// reverse order at the explicit Store edit. A remote runtime applies
// its own credential; this runtime needs no apply rule for it.
func (o *inferenceOperator) ensureHostedApplyRule(by rwire.Subject, entry iwire.HostEntry, why string) error {
	if !entry.Hosted || entry.Credential == "" || entry.Kind == router.WireRemote {
		return nil
	}
	return o.permitRuleWhy(by, o.credentials.operators[0], credentials.ActionApply, credentials.ResourceFor(entry.Credential), why)
}

// credentialStored joins a newly stored credential to existing declarations.
// Listing remains observational, and existing deny rules remain authoritative.
func (o *inferenceOperator) credentialStored(ctx context.Context, caller cwire.Subject, name string) error {
	var errs []error
	for _, f := range o.hostFiles() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if f.Disabled || f.Declaration.Host.Credential != name {
			continue
		}
		errs = append(errs, o.ensureHostedApplyRule(rwire.Subject{Account: caller.Account, Program: caller.Program}, f.hostEntry(), credentialStoreWhy))
	}
	return errors.Join(errs...)
}

// hostedDownReason turns the router's own credential:<outcome>:<name> down
// reason into one naming the subject the missing rule is actually for: a
// hosted host's survey always applies its credential as runtimeProgram, the
// runtime's own operator program, never the program asking for this list, so
// a credential refusal here is always about that program's own rule.
func hostedDownReason(why, credentialName, runtimeProgram string) string {
	if credentialName == "" {
		return why
	}
	const prefix = "credential:"
	suffix := ":" + credentialName
	if !strings.HasPrefix(why, prefix) || !strings.HasSuffix(why, suffix) {
		return why
	}
	outcome := strings.TrimSuffix(strings.TrimPrefix(why, prefix), suffix)
	return fmt.Sprintf("credential:%s for %s", outcome, runtimeProgram)
}

// permitRuleWhy writes one exact permit rule with why as its provenance, unless
// a rule on that target exists.
func (o *inferenceOperator) permitRuleWhy(by rwire.Subject, program, action, resource, why string) error {
	r := o.credentials.runtimeRights
	subject := rwire.Subject{Account: r.owner, Program: program}
	permit := true
	for attempt := 0; attempt < installationAttempts; attempt++ {
		existing := r.policy.ReadRule(subject, action, resource)
		switch existing.Outcome {
		case rwire.RuleReadOutcomeFound, rwire.RuleReadOutcomeExpired:
			return nil
		case rwire.RuleReadOutcomeUnknown:
		default:
			return fmt.Errorf("%s on %s for %s: read %s", action, resource, program, existing.Outcome)
		}
		edit, err := r.policy.ChangeRule(existing.Revision, subject, action, resource, rights.RuleEdit{Permit: &permit, By: by, Why: why}, func() error { return nil })
		if err != nil {
			return fmt.Errorf("%s on %s for %s: %w", action, resource, program, err)
		}
		switch edit.Outcome {
		case rwire.PolicyEditOutcomeApplied:
			return nil
		case rwire.PolicyEditOutcomeConflict:
			continue
		}
		return fmt.Errorf("%s on %s for %s: %s", action, resource, program, edit.Outcome)
	}
	return fmt.Errorf("%s on %s for %s: the policy kept changing", action, resource, program)
}

func (o *inferenceOperator) RemoveHost(ctx context.Context, caller inference.Subject, expected, name string) iwire.HostChange {
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		return iwire.HostChange{Outcome: inferenceEditRefusal(word)}
	}
	if !inferenceHostName.MatchString(name) {
		return iwire.HostChange{Outcome: iwire.EditOutcomeInvalid, Reason: "name"}
	}
	if o.providers == nil {
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	// Only a host leaves through RemoveHost; a provider or a remote runtime
	// leaves through the registry's own Withdraw.
	if !slices.ContainsFunc(o.hostFiles(), func(f providerFile) bool { return f.Declaration.Name == name && !f.Disabled }) {
		revision, err := o.hostRevision()
		if err != nil {
			o.report(err)
			return iwire.HostChange{Outcome: iwire.EditOutcomeUnavailable}
		}
		if expected != revision {
			return iwire.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: revision, Reason: "revision"}
		}
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnknown, Revision: revision}
	}
	withdrawn := o.providers.remove(expected, name)
	if withdrawn.Outcome != wire.DeclarationEditOutcomeApplied {
		return iwire.HostChange{Outcome: hostEditOf(withdrawn.Outcome), Revision: withdrawn.Revision, Reason: withdrawn.Reason}
	}
	o.refreshHosts()
	return iwire.HostChange{Outcome: iwire.EditOutcomeApplied, Revision: withdrawn.Revision, Reason: withdrawn.Reason}
}

// holderRecords lists the runtime account's holder records by name.
func (o *inferenceOperator) holderRecords() (map[string]cwire.Metadata, error) {
	out := map[string]cwire.Metadata{}
	cursor := ""
	for {
		page := o.credentials.holder.List(o.credentials.owner, cursor, 64)
		if page.Outcome != cwire.PageOutcomePage {
			return nil, fmt.Errorf("inference: holder list %s", page.Outcome)
		}
		for _, r := range page.Records {
			out[r.Name] = r
		}
		if page.Complete {
			return out, nil
		}
		cursor = page.Next
	}
}

func keyState(r cwire.Metadata, held bool) iwire.KeyState {
	switch {
	case !held || r.State == cwire.StateRevoked || r.State == cwire.StateExpired:
		return iwire.KeyStateRevoked
	case r.State == cwire.StateLost:
		return iwire.KeyStateLost
	}
	return iwire.KeyStateActive
}

func (o *inferenceOperator) Keys(ctx context.Context, caller inference.Subject) iwire.KeyList {
	if word := o.gate(ctx, caller, inference.ActionKeyIssue); word != "" {
		return iwire.KeyList{Outcome: inferenceListRefusal(word), Keys: []iwire.LocalKey{}}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	records, err := o.holderRecords()
	if err != nil {
		o.report(err)
		return iwire.KeyList{Outcome: iwire.ListOutcomeUnavailable, Keys: []iwire.LocalKey{}}
	}
	list := iwire.KeyList{Outcome: iwire.ListOutcomePage, Keys: []iwire.LocalKey{}}
	for _, k := range o.keys.Keys {
		held, ok := records[k.Name]
		list.Keys = append(list.Keys, iwire.LocalKey{Program: k.Program, Name: k.Name, Credential: k.Credential, IssuedUnixMs: k.IssuedUnixMS, IssuedBy: k.IssuedBy, State: keyState(held, ok)})
	}
	return list
}

func (o *inferenceOperator) IssueKey(ctx context.Context, caller inference.Subject, program, credential string) iwire.KeyIssued {
	if word := o.gate(ctx, caller, inference.ActionKeyIssue); word != "" {
		return iwire.KeyIssued{Outcome: inferenceEditRefusal(word)}
	}
	switch {
	case !identity.ValidSubjectProgram(program):
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeInvalid, Reason: "program"}
	case credential != "" && !credentialName.MatchString(credential):
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeInvalid, Reason: "credential"}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	records, err := o.holderRecords()
	if err != nil {
		o.report(err)
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeUnavailable}
	}
	for _, k := range o.keys.Keys {
		if held, ok := records[k.Name]; samePrograms(k.Program, program) && keyState(held, ok) == iwire.KeyStateActive {
			return iwire.KeyIssued{Outcome: iwire.EditOutcomeConflict, Reason: "active key"}
		}
	}
	if len(o.keys.Keys) >= 256 {
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeInvalid, Reason: "keys"}
	}
	name, key, err := mintLocalKey(program)
	if err != nil {
		o.report(err)
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeUnavailable}
	}
	stored := o.credentials.holder.StoreLocalKey(cwire.Subject{Account: caller.Account, Program: caller.Program}, cwire.Registration{Name: name, Kind: credentials.KindLocalKey,
		Scope: cwire.Scope{Targets: []string{"localhost"}, Consumers: []string{inference.Contract}}, Secret: []byte(key)})
	switch stored.Outcome {
	case cwire.StoreOutcomeStored:
	case cwire.StoreOutcomeNoSecureStore:
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeNoSecureStore}
	case cwire.StoreOutcomeExhausted:
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeInvalid, Reason: "holder exhausted"}
	default:
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeUnavailable, Reason: "holder:" + stored.Outcome.String()}
	}
	record := keyRecord{Program: program, Name: name, Credential: credential, IssuedUnixMS: time.Now().UnixMilli(), IssuedBy: caller.Program}
	o.keys.Keys = append(slices.DeleteFunc(o.keys.Keys, func(k keyRecord) bool {
		held, ok := records[k.Name]
		return samePrograms(k.Program, program) && keyState(held, ok) != iwire.KeyStateActive
	}), record)
	if err := writeAtomically(filepath.Join(o.dir, inferenceKeysFile), o.keys); err != nil {
		o.report(err)
		o.keys.Keys = o.keys.Keys[:len(o.keys.Keys)-1]
		o.credentials.holder.Revoke(cwire.Subject{Account: caller.Account, Program: caller.Program}, stored.Revision, name)
		return iwire.KeyIssued{Outcome: iwire.EditOutcomeUnavailable}
	}
	return iwire.KeyIssued{Outcome: iwire.EditOutcomeApplied, Key: key, Record: &iwire.LocalKey{Program: program, Name: name, Credential: credential,
		IssuedUnixMs: record.IssuedUnixMS, IssuedBy: record.IssuedBy, State: iwire.KeyStateActive}}
}

func (o *inferenceOperator) RevokeKey(ctx context.Context, caller inference.Subject, program string) iwire.KeyRevoked {
	if word := o.gate(ctx, caller, inference.ActionKeyIssue); word != "" {
		return iwire.KeyRevoked{Outcome: inferenceEditRefusal(word)}
	}
	if !identity.ValidSubjectProgram(program) {
		return iwire.KeyRevoked{Outcome: iwire.EditOutcomeInvalid}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	records, err := o.holderRecords()
	if err != nil {
		o.report(err)
		return iwire.KeyRevoked{Outcome: iwire.EditOutcomeUnavailable}
	}
	for _, k := range o.keys.Keys {
		held, ok := records[k.Name]
		if !samePrograms(k.Program, program) || keyState(held, ok) == iwire.KeyStateRevoked {
			continue
		}
		result := o.credentials.holder.Revoke(cwire.Subject{Account: caller.Account, Program: caller.Program}, held.Revision, k.Name)
		switch result.Outcome {
		case cwire.RevokeOutcomeRevoked:
			return iwire.KeyRevoked{Outcome: iwire.EditOutcomeApplied}
		case cwire.RevokeOutcomeUnknown:
			return iwire.KeyRevoked{Outcome: iwire.EditOutcomeUnknown}
		}
		return iwire.KeyRevoked{Outcome: iwire.EditOutcomeUnavailable}
	}
	return iwire.KeyRevoked{Outcome: iwire.EditOutcomeUnknown}
}

func (o *inferenceOperator) Audit(ctx context.Context, caller inference.Subject, cursor, maxEntries int64) iwire.AuditPage {
	if word := o.gate(ctx, caller, inference.ActionAuditRead); word != "" {
		return iwire.AuditPage{Outcome: inferenceAuditRefusal(word), Entries: []iwire.AuditEntry{}, Next: cursor}
	}
	return o.journal.Page(cursor, maxEntries)
}
