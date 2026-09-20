package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"strings"
	"testing"

	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// No serve test reads a real product record: every runtime a test composes
// probes an empty fixture machine unless the test sets its own.
func init() { declarationEnv = fixtureDeclarations(nil, nil) }

func fixtureDeclarations(files, env map[string]string) func(func(error)) router.ProbeEnv {
	return func(report func(error)) router.ProbeEnv {
		return router.ProbeEnv{GOOS: "linux", Home: "/fixture-home",
			Getenv: func(name string) string { return env[name] },
			ReadFile: func(path string) ([]byte, error) {
				if body, ok := files[path]; ok {
					return []byte(body), nil
				}
				return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
			},
			Status: func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("not on the fixture machine")
			},
			Log: func(product, reason string) {
				if report != nil {
					report(errors.New(product + ": " + reason))
				}
			}}
	}
}

// A runtime without local entries lists the hosts products declare, with
// declared_by; an operator's host joins them; removing a declared host keeps
// the rest as entries and stops declarations.
func TestRuntimeListsProductDeclaredHosts(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	upstream, _ := fakeOllama(t)
	u, _ := url.Parse(upstream.URL)
	saved := declarationEnv
	declarationEnv = fixtureDeclarations(map[string]string{"/fixture-home/.lmstudio/.internal/http-server-config.json": `{"port":` + u.Port() + `}`},
		map[string]string{"OLLAMA_HOST": u.Host})
	t.Cleanup(func() { declarationEnv = saved })
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	out, err := runInference(t, append([]string{"host", "list"}, endpoint...)...)
	if err != nil {
		t.Fatalf("host list: %v\n%s", err, out)
	}
	for _, want := range []string{"lemonade", "lmstudio", "ollama"} {
		if !strings.Contains(out, want) {
			t.Fatalf("host list lacks %s:\n%s", want, out)
		}
	}
	list := hostListJSON(t, endpoint)
	byName := map[string]iwire.HostState{}
	for _, h := range list.Hosts {
		byName[h.Entry.Name] = h
	}
	if h := byName["ollama"]; h.Entry.DeclaredBy != "ollama" || h.Entry.Base != "http://"+u.Host {
		t.Fatalf("ollama %+v", h)
	}
	if h := byName["lmstudio"]; h.Entry.DeclaredBy != "lmstudio" || h.Entry.Base != "http://127.0.0.1:"+u.Port() {
		t.Fatalf("lmstudio %+v", h)
	}
	if h := byName["lemonade"]; h.Entry.DeclaredBy != "default" || h.Entry.Base != router.LemonadeDefaultBase {
		t.Fatalf("lemonade %+v", h)
	}
	if out, err := runInference(t, append([]string{"host", "remove", "lemonade"}, endpoint...)...); err != nil {
		t.Fatalf("remove a declared host: %v\n%s", err, out)
	}
	list = hostListJSON(t, endpoint)
	var names []string
	for _, h := range list.Hosts {
		names = append(names, h.Entry.Name+"/"+h.Entry.DeclaredBy)
	}
	if strings.Join(names, " ") != "lmstudio/lmstudio ollama/ollama comfyui/default" {
		t.Fatalf("after removing lemonade: %v", names)
	}
}

func hostListJSON(t *testing.T, endpoint []string) iwire.HostList {
	t.Helper()
	out, err := runInference(t, append([]string{"host", "list", "--json"}, endpoint...)...)
	if err != nil {
		t.Fatalf("host list: %v\n%s", err, out)
	}
	var list iwire.HostList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("host list json: %v\n%s", err, out)
	}
	return list
}
