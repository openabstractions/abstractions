package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
)

// AddHost entries: profiles are seeded or owned words, declared_by belongs to
// the runtime, and a ceiling needs a credential.
func TestInferenceHostEntryValidation(t *testing.T) {
	hosted := iwire.HostEntry{Name: "openrouter", Hosted: true, Kind: "openai-compatible", Base: "https://openrouter.ai/api/v1", Credential: "openrouter"}
	for _, c := range []struct {
		edit  func(*iwire.HostEntry)
		field string
	}{
		{func(*iwire.HostEntry) {}, ""},
		{func(e *iwire.HostEntry) { e.Profiles = []string{"chat", "example/rerank@1"} }, ""},
		{func(e *iwire.HostEntry) { e.Profiles = []string{"chat", "chat"} }, "profiles"},
		{func(e *iwire.HostEntry) { e.Profiles = []string{"vision"} }, "profiles"},
		{func(e *iwire.HostEntry) { e.DeclaredBy = "operator" }, "declared_by"},
		{func(e *iwire.HostEntry) { e.Ceiling = &iwire.CeilingLimit{ImagesPerDay: -1} }, "ceiling"},
		{func(e *iwire.HostEntry) { e.Base = "http://openrouter.ai/api/v1" }, "base"},
		{func(e *iwire.HostEntry) { e.Hosted, e.Kind, e.Credential = false, "ollama", "" }, "name"},
	} {
		entry := hosted
		c.edit(&entry)
		if got := validEntry(entry); got != c.field {
			t.Fatalf("%+v: invalid field %q, want %q", entry, got, c.field)
		}
	}
	if got := defaultProfiles(true, "anthropic-messages"); !slices.Equal(got, []string{"chat"}) {
		t.Fatalf("anthropic default %v", got)
	}
	if got := defaultProfiles(false, ""); !slices.Equal(got, iwire.HostProfiles) {
		t.Fatalf("local default %v", got)
	}
}

// defaultProfiles' hosted branch defers to router.DefaultProfiles for every
// wire but openai-compatible (research/inference-modalities/LIVE-MEASUREMENT-2026-09-22.md
// found it hardcoding "chat" for every one of them, wrong for
// openai-realtime and the other specialized wires): one case per
// router.thrift wire_kinds member a hosted host may declare, and, for every
// one but openai-compatible, agreement with the router's own table, so the
// two can never drift apart again. openai-compatible keeps every seeded
// profile, the same answer the local kinds give, unchanged by this task.
func TestDefaultProfilesAgreesWithTheRouterPerWireKind(t *testing.T) {
	for _, c := range []struct {
		wire string
		want []string
	}{
		{router.WireOpenAICompatible, iwire.HostProfiles},
		{router.WireOpenAIRealtime, []string{"live"}},
		{router.WireAnthropicMessages, []string{"chat"}},
		{router.WireDeepgramPrerecorded, []string{"transcription"}},
		{router.WireElevenLabsStream, []string{"speech"}},
		{router.WireStabilityV2Beta, []string{"image"}},
		{router.WireFalQueue, []string{"image"}},
		{router.WireReplicatePredictions, []string{"image"}},
	} {
		if got := defaultProfiles(true, c.wire); !slices.Equal(got, c.want) {
			t.Errorf("defaultProfiles(true, %q) = %v, want %v", c.wire, got, c.want)
		}
		if c.wire == router.WireOpenAICompatible {
			continue
		}
		// The operator's answer is never anything but the router's own table
		// for the same wire, called through the same *router.Host
		// construction the router routes through.
		if got, fromRouter := defaultProfiles(true, c.wire), router.DefaultProfiles(router.NewHosted("", "", c.wire, "")); !slices.Equal(got, fromRouter) {
			t.Errorf("defaultProfiles(true, %q) = %v, disagrees with router.DefaultProfiles = %v", c.wire, got, fromRouter)
		}
	}
	if !slices.Contains(hostedWireKinds, router.WireRemote) {
		// oa-remote@1 is its own declaration role (validEntry never sees it
		// as a hosted host's kind); defaultProfiles is never called for it.
		t.Log("oa-remote@1 confirmed excluded from hostedWireKinds, as validEntry expects")
	} else {
		t.Fatalf("hostedWireKinds must exclude %q", router.WireRemote)
	}
}

// hostedDownReason names the subject a missing runtime apply rule actually
// refused: a hosted host's survey always applies its credential as the
// runtime's own operator program, never the program asking to list the
// hosts, so the router's own credential:<outcome>:<name> reason is rewritten
// to name that program instead of repeating the (already known) credential
// name.
func TestHostedDownReasonNamesTheRuntimeProgram(t *testing.T) {
	const program = "/usr/bin/openabstractions"
	for _, c := range []struct{ why, credential, want string }{
		{"credential:not_permitted:openrouter", "openrouter", "credential:not_permitted for " + program},
		{"credential:unavailable:openrouter", "openrouter", "credential:unavailable for " + program},
		{"credential:revoked:openrouter", "openrouter", "credential:revoked for " + program},
		{"router:no-host", "openrouter", "router:no-host"},
		{"not surveyed yet", "openrouter", "not surveyed yet"},
		// A different host's credential name: never rewritten on this one's.
		{"credential:not_permitted:other", "openrouter", "credential:not_permitted:other"},
		// A local host, or a hosted host with no credential: never rewritten.
		{"credential:not_permitted:openrouter", "", "credential:not_permitted:openrouter"},
	} {
		if got := hostedDownReason(c.why, c.credential, program); got != c.want {
			t.Errorf("hostedDownReason(%q, %q, %q) = %q, want %q", c.why, c.credential, program, got, c.want)
		}
	}
}

// An explicit Store reconciles a previously declared host before replying.
// Hosts only reads the resulting policy. The reverse order is covered by
// TestRuntimeGatewayWindowServesAKeyedProgram.
func TestCredentialStoreGrantsHostedSurveyWithoutListing(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	const credName = "openrouter"
	config := fmt.Sprintf(`{"local":[],"hosted":[{"name":%q,"base":"https://openrouter.invalid/api/v1","wire":"openai-compatible","credential":%q}]}`, credName, credName)
	for _, file := range []struct{ path, body string }{
		{filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(options.stateDir, "inference", inferenceHostsFile), config},
	} {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, []byte(file.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	startInferenceRuntime(t, options)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = filepath.Clean(self)
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	call, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	rightsOperator, err := client.New(options.endpoint).ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	subject := rights.Subject{Account: account.Uid, Program: self}
	resource := credentials.ResourceFor(credName)
	read := func() rights.RuleRead {
		t.Helper()
		got, err := rightsOperator.ReadRuleContext(call, subject, credentials.ActionApply, resource)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); got.Outcome.String() != "unknown" {
		t.Fatalf("apply rule before the host was ever declared through AddHost or listed: %+v", got)
	}

	// Register the credential, granting apply to a client program that is
	// never the runtime's own: the runtime's survey of a hosted host always
	// applies its credential as the runtime's own operator program
	// (composeInference's UseCredentials), a distinct identity from any
	// client this grants.
	other := filepath.Join(t.TempDir(), "other-client")
	if runtime.GOOS == "windows" {
		other += ".exe"
	}
	out, diag, err := runCredentials(t, testCredentialSecret+"\n", "add", credName, "--target", "openrouter.invalid",
		"--for", inference.Contract, "--for", router.CredentialConsumer, "--use-by", other, "--from-stdin",
		"--endpoint", options.endpoint, "--timeout", "30s")
	requireUserScopeAdd(t, err, out+diag)

	storedRule := read()
	if storedRule.Outcome.String() != "found" || !storedRule.Record.Rule.Permit {
		t.Fatalf("credential Store did not establish the hosted survey rule: %+v", storedRule)
	}

	// Listings must leave the policy revision unchanged.
	if out, err := runInference(t, "host", "list", "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatalf("host list: %v\n%s", err, out)
	}
	if got := read(); got.Outcome.String() != "found" || !got.Record.Rule.Permit || got.Revision != storedRule.Revision {
		t.Fatalf("apply rule after listing: %+v", got)
	}

	// A second read remains observational.
	if out, err := runInference(t, "host", "list", "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatalf("second host list: %v\n%s", err, out)
	}
	if got := read(); got.Outcome.String() != "found" || !got.Record.Rule.Permit || got.Revision != storedRule.Revision {
		t.Fatalf("apply rule after a second listing: %+v", got)
	}
}

// A ceiling record keeps every unit field, enforced or not, so the state file
// needs no migration when a profile starts counting requests, images, audio
// seconds or characters.
func TestCeilingRecordsCarryEveryUnit(t *testing.T) {
	limit := ceilingOf(&iwire.CeilingLimit{TokensPerDay: 1, MicrosPerDay: 2, RequestsPerDay: 3, ImagesPerDay: 4, AudioSecondsPerDay: 5, CharactersPerDay: 6})
	raw, err := json.Marshal(inferenceHosts{Ceilings: map[string]inference.Ceiling{"openrouter": limit}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"tokens_per_day":1`, `"micros_per_day":2`, `"requests_per_day":3`, `"images_per_day":4`, `"audio_seconds_per_day":5`, `"characters_per_day":6`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("%s lacks %s", raw, field)
		}
	}
	var back inferenceHosts
	if err := json.Unmarshal(raw, &back); err != nil || *ceilingLimit(back.Ceilings["openrouter"]) != (iwire.CeilingLimit{TokensPerDay: 1, MicrosPerDay: 2, RequestsPerDay: 3, ImagesPerDay: 4, AudioSecondsPerDay: 5, CharactersPerDay: 6}) {
		t.Fatalf("round trip %+v %v", back, err)
	}
}
