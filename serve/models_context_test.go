package main

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type fakeTypedContext struct {
	values map[string]string
	err    error
}

func (f fakeTypedContext) ReadTypedContext(context.Context, []string) (configwire.ReadResult, error) {
	if f.err != nil {
		return configwire.ReadResult{}, f.err
	}
	return configwire.ReadResult{Outcome: configwire.ReadOutcomeRead, Entries: []configwire.Entry{
		{Key: modelContextKey, Value: &configwire.Value{Kind: configwire.ValueKindTextMap, TextMap: f.values}}}}, nil
}

// The inventory reports the context one conversation gets: the model host's
// whole engine context divided by its slots. Qwen3.6 35B on a machine whose
// memory is unknown runs --ctx-size 65536 over --parallel 2 and reports 32768.
func TestHostedContextsReportOneConversation(t *testing.T) {
	entries := codeCatalogue(context.Background())
	if len(entries) == 0 {
		t.Fatal("no curated code entry")
	}
	entry := entries[0]
	pin := entry.Digest + ":" + strconv.FormatInt(entry.DownloadBytes, 10)
	models := []hostedModel{{Name: "qwen3.6-35b-a3b", Store: "hf", Object: "weights", Content: pin}, {Name: "unpinned", Store: "hf", Object: "other"}}
	for _, tc := range []struct {
		name   string
		pool   int64
		choice fakeTypedContext
		want   int64
	}{
		{"unknown-memory", 0, fakeTypedContext{}, 32768},
		{"large-machine-automatic", 128 << 30, fakeTypedContext{}, 262144},
		{"large-machine-chosen", 128 << 30, fakeTypedContext{values: map[string]string{entry.Family: "65536"}}, 65536},
		{"choice-unreadable", 128 << 30, fakeTypedContext{err: errors.New("no service")}, 262144},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choice := modelContextChoice(func() (typedContextReader, error) { return tc.choice, nil })
			read := hostedContexts(models, entries, planConversationContext(func() int64 { return tc.pool }, choice))
			got := read(context.Background(), []string{"qwen3.6-35b-a3b", "unpinned"})
			if got["qwen3.6-35b-a3b"] != tc.want || len(got) != 1 {
				t.Fatalf("contexts %v, want qwen at %d only", got, tc.want)
			}
		})
	}
}

// The model host is OA's own provider: its registration names model_context,
// the runtime accepts that catalogue key, and the Keep its launch runs writes
// the exact read rule, so the quiet key-read decision permits it and no person
// question is ever needed. Before Keep the quiet decision refuses.
func TestModelHostContextReadIsQuietForItsOwnRegistration(t *testing.T) {
	ctx := t.Context()
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	known := func(key string) bool {
		for _, d := range configwire.ConfigKeyDescriptors() {
			if d.Key == key {
				return true
			}
		}
		return false
	}
	spec := registration.RegistrationSpec{Providers: []registration.ProviderFacet{{Name: modelHostName, ConfigKeys: []string{modelContextKey}}}}
	keys, err := providerConfigKeys(spec, known)
	if err != nil || len(keys) != 1 || keys[0] != modelContextKey {
		t.Fatalf("keys %v, %v", keys, err)
	}
	program := filepath.Join(t.TempDir(), modelHostProgram+".exe")
	subject := rwire.Subject{Account: r.owner, Program: program}
	if d := r.decide(ctx, subject, host.ConfigKeyReadAction, modelContextKey); d.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("read permitted before Keep")
	}
	if err := r.keepProviderConfig(ctx, program, keys); err != nil {
		t.Fatal(err)
	}
	if d := r.decide(ctx, subject, host.ConfigKeyReadAction, modelContextKey); d.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatalf("model host read %s after Keep", d.Outcome)
	}
	var _ host.QuietDecider = r
}
