package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	router "github.com/openabstractions/abstraction-router/go"
)

// A model pulled onto a declared server announces nothing. The composed
// runtime's survey clock (routerSurveyEvery) brings it into a fresh: false
// Offer within one interval, with no caller forcing a survey.
func TestRuntimeRouterSurveyClockShowsPulledModelInNonFreshOffer(t *testing.T) {
	var mu sync.Mutex
	models := []string{"fixture-chat:1b"}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		names := make([]string, len(models))
		for i, m := range models {
			names[i] = fmt.Sprintf(`{"name":%q}`, m)
		}
		fmt.Fprintf(w, `{"models":[%s]}`, strings.Join(names, ","))
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"models":[]}`)) })
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	every := 200 * time.Millisecond
	previous := routerSurveyEvery
	routerSurveyEvery = every
	t.Cleanup(func() { routerSurveyEvery = previous })

	options, _ := isolatedRuntime(t)
	saved := declarationEnv
	declarationEnv = fixtureDeclarations(nil, map[string]string{"OLLAMA_HOST": upstream.URL})
	t.Cleanup(func() { declarationEnv = saved })
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"declared":true}`
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	rightsState, err := composeRights(options)
	if err != nil {
		t.Fatal(err)
	}
	report := func(error) {}
	credentials, err := composeCredentials(options, rightsState, report)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		assertListenersReleased(t, options)
		t.Skip("the composed runtime requires Program proof; Unix startup refusal is checked separately")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.close()
	composed, err := composeInference(options, credentials, nil, report)
	if err != nil {
		t.Fatal(err)
	}
	defer composed.Close()

	// The router folds a server's name into its family; the alias is a name.
	offered := func(name string) bool {
		offers, _, err := composed.router.Offer(router.Request{Placement: router.PlacementLocal, Reserve: 4096})
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(offers, func(o router.Offer) bool { return slices.Contains(o.Names, name) })
	}
	waitOffered := func(name string, bound time.Duration) time.Duration {
		start := time.Now()
		for time.Since(start) < bound {
			if offered(name) {
				return time.Since(start)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s is not in a non-fresh Offer after %v", name, bound)
		return 0
	}
	waitOffered("fixture-chat:1b", 5*time.Second)
	if offered("pulled-chat:2b") {
		t.Fatal("a model not yet pulled is offered")
	}
	mu.Lock()
	models = append(models, "pulled-chat:2b")
	mu.Unlock()
	// One interval plus the survey's own reads; generous for a loaded machine.
	took := waitOffered("pulled-chat:2b", 10*every+2*time.Second)
	t.Logf("pulled model offered after %v (clock %v)", took, every)
}
