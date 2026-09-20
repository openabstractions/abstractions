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

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// Files the inference operator keeps beside hosts.json in <state>/inference.
const (
	inferenceJournalFile = "audit.jsonl"
	inferenceKeysFile    = "keys.json"
	// localKeyPrefix begins every local key; the name tag follows it.
	localKeyPrefix = "oalk_"
	// hostAddWhy is the provenance of the rules AddHost writes.
	hostAddWhy = "inference host add"
	// hostsSurveyAge re-reads the hosts for a listing older than this.
	hostsSurveyAge = 30 * time.Second
	// declaredByOperator is the declared_by of a host added through operator@1.
	declaredByOperator = "operator"
)

// defaultProfiles is what a host serves when its entry names no profiles:
// every seeded profile on the OpenAI-compatible wire, which the local kinds
// speak, and chat on any other wire.
func defaultProfiles(hosted bool, wire string) []string {
	if !hosted || wire == router.WireOpenAICompatible {
		return slices.Clone(iwire.HostProfiles)
	}
	return []string{"chat"}
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
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

// localHosts is the effective local host list: the configured entries, then
// the hosts products declare that no entry names.
func (o *inferenceOperator) localHosts(config inferenceHosts) []inferenceLocalHost {
	var out []inferenceLocalHost
	if config.Local != nil {
		out = append(out, *config.Local...)
	}
	if !config.usesDeclarations() {
		return out
	}
	for _, h := range declaredLocalHosts(o.report) {
		if !slices.ContainsFunc(out, func(l inferenceLocalHost) bool { return l.Kind == h.Name }) {
			out = append(out, inferenceLocalHost{Kind: h.Name, Base: h.Base, DeclaredBy: h.DeclaredBy})
		}
	}
	return out
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
	if e.Kind != router.WireOpenAICompatible && e.Kind != router.WireAnthropicMessages && !ownedWireKind.MatchString(e.Kind) {
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

// apply writes config, gives the router its hosts and the ceilings their
// limits, and surveys the hosts in the background.
func (o *inferenceOperator) apply(config inferenceHosts) (string, error) {
	hosts, err := inferenceHostList(config, o.report)
	if err != nil {
		return "", err
	}
	if err := writeAtomically(filepath.Join(o.dir, inferenceHostsFile), config); err != nil {
		return "", err
	}
	if o.providers != nil {
		hosts = append(hosts, o.providers.nativeInferenceHosts()...)
		hosts = append(hosts, o.providers.remoteHosts()...)
	}
	o.router.SetHosts(hosts...)
	for name, limit := range config.Ceilings {
		if err := o.ceilings.SetLimit(name, limit); err != nil {
			o.report(err)
		}
	}
	go o.router.Survey()
	_, revision, err := o.readConfig()
	return revision, err
}

// refreshHosts gives the router the configured hosts and the remote
// declarations again, after a remote declaration changed.
func (o *inferenceOperator) refreshHosts() {
	o.mu.Lock()
	defer o.mu.Unlock()
	config, _, err := o.readConfig()
	if err == nil {
		var hosts []*router.Host
		if hosts, err = inferenceHostList(config, o.report); err == nil {
			providerHosts := 0
			if o.providers != nil {
				native := o.providers.nativeInferenceHosts()
				remote := o.providers.remoteHosts()
				providerHosts = len(native) + len(remote)
				hosts = append(hosts, native...)
				hosts = append(hosts, remote...)
			}
			o.router.SetHosts(hosts...)
			if providerHosts > 0 {
				o.router.Survey()
			}
		}
	}
	if err != nil {
		o.report(err)
	}
}

func (o *inferenceOperator) Hosts(ctx context.Context, caller inference.Subject) iwire.HostList {
	refused := func(outcome iwire.ListOutcome) iwire.HostList {
		return iwire.HostList{Outcome: outcome, Hosts: []iwire.HostState{}}
	}
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		return refused(inferenceListRefusal(word))
	}
	o.mu.Lock()
	config, revision, err := o.readConfig()
	o.mu.Unlock()
	if err != nil {
		o.report(err)
		return refused(iwire.ListOutcomeUnavailable)
	}
	_, _, _, _, at := o.router.Residency(false)
	states, _, _, _, _ := o.router.Residency(time.Since(at) > hostsSurveyAge)
	byName := map[string]router.HostState{}
	for _, s := range states {
		byName[s.Host] = s
	}
	state := func(entry iwire.HostEntry) iwire.HostState {
		s, surveyed := byName[entry.Name]
		out := iwire.HostState{Entry: entry, Up: s.Up, Why: s.Why}
		if !surveyed {
			out.Why = "not surveyed yet"
		}
		if entry.Credential != "" {
			day, tokens, micros, requests, images, audioSeconds, characters := o.ceilings.SpendUnits(entry.Credential)
			out.Spend = &iwire.Spend{Day: day, Tokens: tokens, Micros: micros, Requests: requests, Images: images, AudioSeconds: audioSeconds, Characters: characters}
		}
		return out
	}
	list := iwire.HostList{Outcome: iwire.ListOutcomePage, Revision: revision, Hosts: []iwire.HostState{}}
	profiles := func(stored []string, hosted bool, wire string) []string {
		if len(stored) == 0 {
			return defaultProfiles(hosted, wire)
		}
		return stored
	}
	for _, l := range o.localHosts(config) {
		list.Hosts = append(list.Hosts, state(iwire.HostEntry{Name: l.Kind, Kind: l.Kind, Base: l.Base, Profiles: profiles(l.Profiles, false, ""), DeclaredBy: l.DeclaredBy}))
	}
	for _, h := range config.Hosted {
		entry := iwire.HostEntry{Name: h.Name, Hosted: true, Kind: h.Wire, Base: h.Base, Credential: h.Credential, Profiles: profiles(h.Profiles, true, h.Wire), DeclaredBy: h.DeclaredBy}
		if limit, ok := config.Ceilings[h.Credential]; ok && h.Credential != "" {
			entry.Ceiling = ceilingLimit(limit)
		}
		list.Hosts = append(list.Hosts, state(entry))
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
	o.mu.Lock()
	defer o.mu.Unlock()
	config, revision, err := o.readConfig()
	if err != nil {
		o.report(err)
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	if expected != revision {
		return iwire.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	local := o.localHosts(config)
	for _, l := range local {
		if l.Kind == entry.Name {
			return iwire.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: revision, Reason: "name"}
		}
	}
	for _, h := range config.Hosted {
		if h.Name == entry.Name {
			return iwire.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: revision, Reason: "name"}
		}
	}
	if len(local)+len(config.Hosted) >= 64 {
		return iwire.HostChange{Outcome: iwire.EditOutcomeInvalid, Reason: "hosts"}
	}
	if len(entry.Profiles) == 0 {
		entry.Profiles = defaultProfiles(entry.Hosted, entry.Kind)
	}
	if entry.Hosted {
		config.Hosted = append(config.Hosted, inferenceHostedHost{Name: entry.Name, Base: entry.Base, Wire: entry.Kind, Credential: entry.Credential,
			Profiles: entry.Profiles, DeclaredBy: declaredByOperator})
		if c := entry.Ceiling; c != nil {
			if config.Ceilings == nil {
				config.Ceilings = map[string]inference.Ceiling{}
			}
			config.Ceilings[entry.Credential] = ceilingOf(c)
		}
	} else {
		// Declarations keep adding hosts beside the operator's entries.
		declared := config.usesDeclarations()
		entries := []inferenceLocalHost{}
		if config.Local != nil {
			entries = append(entries, *config.Local...)
		}
		entries = append(entries, inferenceLocalHost{Kind: entry.Kind, Base: entry.Base, Profiles: entry.Profiles, DeclaredBy: declaredByOperator})
		config.Local, config.Declared = &entries, &declared
	}
	revision, err = o.apply(config)
	if err != nil {
		o.report(err)
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	change := iwire.HostChange{Outcome: iwire.EditOutcomeApplied, Revision: revision}
	if err := o.writeHostRules(caller, entry); err != nil {
		o.report(err)
		change.Reason = "rules:" + err.Error()
	}
	return change
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
	// A remote runtime applies its own credential; this runtime needs no apply rule.
	if entry.Hosted && entry.Credential != "" && entry.Kind != router.WireRemote {
		errs = append(errs, o.permitRule(by, r.operators[0], credentials.ActionApply, credentials.ResourceFor(entry.Credential)))
	}
	return errors.Join(errs...)
}

func (o *inferenceOperator) permitRule(by rwire.Subject, program, action, resource string) error {
	return o.permitRuleWhy(by, program, action, resource, hostAddWhy)
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
	o.mu.Lock()
	defer o.mu.Unlock()
	config, revision, err := o.readConfig()
	if err != nil {
		o.report(err)
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	if expected != revision {
		return iwire.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	found := false
	if config.Local != nil {
		entries := *config.Local
		if i := slices.IndexFunc(entries, func(l inferenceLocalHost) bool { return l.Kind == name }); i >= 0 {
			entries = append(slices.Clone(entries[:i]), entries[i+1:]...)
			declared := config.usesDeclarations()
			config.Local, config.Declared, found = &entries, &declared, true
		}
	}
	if !found {
		// Removing a declared host stops declarations; the others stay as entries.
		local := o.localHosts(config)
		if i := slices.IndexFunc(local, func(l inferenceLocalHost) bool { return l.Kind == name }); i >= 0 {
			local = append(slices.Clone(local[:i]), local[i+1:]...)
			declared := false
			config.Local, config.Declared, found = &local, &declared, true
		}
	}
	for i, h := range config.Hosted {
		if h.Name == name {
			config.Hosted, found = slices.Delete(config.Hosted, i, i+1), true
			break
		}
	}
	if !found {
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnknown, Revision: revision}
	}
	if revision, err = o.apply(config); err != nil {
		o.report(err)
		return iwire.HostChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	return iwire.HostChange{Outcome: iwire.EditOutcomeApplied, Revision: revision}
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
