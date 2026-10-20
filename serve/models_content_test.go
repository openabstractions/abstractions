package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestModelsHostContentRequiresCatalogueDigestAndObjectSize(t *testing.T) {
	entry := codeCatalogue(context.Background())[0]
	pin, err := catalogueContent(context.Background(), entry.Digest, entry.DownloadBytes)
	if err != nil || pin == "" {
		t.Fatalf("catalogue pin: %q %v", pin, err)
	}
	for _, test := range []struct {
		digest string
		size   int64
	}{{entry.Digest, entry.DownloadBytes - 1}, {"sha256:" + strings.Repeat("a", 64), entry.DownloadBytes}} {
		if _, err := catalogueContent(context.Background(), test.digest, test.size); err == nil {
			t.Fatalf("unreviewed content accepted: %+v", test)
		}
	}
}

func TestModelsHostExplicitEmptyContentRefusesBeforeRuntimeLookup(t *testing.T) {
	if nativeProviderLimit != "" {
		t.Skip(nativeProviderLimit)
	}
	err := modelsHostCommand("models host", []string{"store/object", "--content="}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--content requires") {
		t.Fatalf("explicit empty pin fell back to an unpinned command: %v", err)
	}
}

func TestModelsContentSurvivesRewriteAndLeavesWithObject(t *testing.T) {
	entry := codeCatalogue(context.Background())[0]
	pin, err := catalogueContent(context.Background(), entry.Digest, entry.DownloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{engine: "engine", endpoint: "provider"}
	models := []hostedModel{{Name: "coding", Store: "oa-chat-fixture", Object: "result", Content: pin}, {Name: "adopted", Store: "external", Object: "other"}}
	args := hostArguments(settings, models, nil)
	read, got := readServeArguments(args)
	read.extra = []string{"--parallel", "1"}
	_, again := readServeArguments(hostArguments(read, got, nil))
	if len(again) != 2 || again[0].Content != pin || again[1].Content != "" {
		t.Fatalf("rewrite changed pins: %+v", again)
	}
	kept := slices.DeleteFunc(again, func(m hostedModel) bool { return m.Store == "oa-chat-fixture" && m.Object == "result" })
	if args := hostArguments(read, kept, nil); slices.Contains(args, argModelContent) {
		t.Fatalf("unhosting retained its pin: %v", args)
	}
	// Source claims and reserved-looking store names never add a content flag.
	_, unpinned := readServeArguments([]string{"serve", "--config", "extra-roots.json", "--model", "coding=oa-chat-spoof/result"})
	if len(unpinned) != 1 || unpinned[0].Content != "" || slices.Contains(hostArguments(settings, unpinned, nil), argModelContent) {
		t.Fatalf("source configuration created a pin: %+v", unpinned)
	}
}

func TestModelsContentInvalidInputsSurviveRewriteForDaemonRefusal(t *testing.T) {
	for _, values := range [][]string{{"store/object="}, {"missing/object=invalid"}, {"store/object=invalid", "store/object=invalid"}} {
		args := []string{"serve", argModel, "coding=store/object"}
		for _, value := range values {
			args = append(args, argModelContent, value)
		}
		settings, models := readServeArguments(args)
		rewritten := hostArguments(settings, models, nil)
		count := 0
		for _, arg := range rewritten {
			if arg == argModelContent {
				count++
			}
		}
		if count != len(values) {
			t.Fatalf("rewrite silently dropped a pin: %v -> %v", args, rewritten)
		}
	}
}
