package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
)

// reviewExamples is private composition for panel_review.ps1's demonstration
// runtime. The existing services admit every registration and host edit.
type reviewExamples struct{ Name, Program, Source string }

func (v reviewExamples) validate(binding *explicitRuntime, panelProgram string) error {
	return v.validateWithEndpoint(binding, panelProgram, bootstrap.Endpoint)
}

func (v reviewExamples) validateWithEndpoint(binding *explicitRuntime, panelProgram string, endpointFor func(string) (string, error)) error {
	if !regexp.MustCompile(`^[a-z0-9-]{1,64}$`).MatchString(v.Name) || binding == nil {
		return errors.New("review examples require an explicitly selected isolated runtime")
	}
	endpoint, err := endpointFor(v.Name + "-runtime")
	if err != nil || endpoint != binding.Endpoint {
		return errors.New("review examples require the named isolated runtime endpoint")
	}
	root := filepath.Dir(panelProgram)
	if !filepath.IsAbs(root) || !identity.SameSubjectProgram(binding.Program, filepath.Join(root, "openabstractions.exe")) ||
		!identity.SameSubjectProgram(v.Program, filepath.Join(root, "pretend-lemonade.exe")) ||
		!filepath.IsAbs(binding.StateDir) || filepath.Base(binding.StateDir) != "state" {
		return errors.New("review examples require the review folder's exact runtime, example program and private state")
	}
	if err := privatedir.CheckDir(binding.StateDir); err != nil {
		return fmt.Errorf("review examples require private runtime state: %w", err)
	}
	for _, path := range []string{panelProgram, binding.Program, v.Program} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("review examples require existing regular review programs")
		}
	}
	u, err := url.Parse(v.Source)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("review examples require the owned loopback example source")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("review examples require the owned loopback example source port")
	}
	return nil
}

func populateReviewExamples(ctx context.Context, v reviewExamples) (string, error) {
	operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return "", err
	}
	list, err := operator.Hosts(ctx)
	if err != nil {
		return "", err
	}
	if list.Outcome != iwire.ListOutcomePage {
		return "", fmt.Errorf("demonstration model servers: %s", list.Outcome.String())
	}
	entry := iclient.HostEntry{Name: "demonstration-model-server", Hosted: true, Kind: "openai-compatible", Base: v.Source + "/v1", Profiles: []string{"chat"}}
	for _, h := range list.Hosts {
		if h.Entry.Name != entry.Name {
			continue
		}
		if h.Entry.Base != entry.Base || !h.Entry.Hosted || h.Entry.Kind != entry.Kind || h.Entry.Credential != "" {
			return "", errors.New("existing demonstration model server differs; it was preserved")
		}
		return registerReviewExample(ctx, v)
	}
	change, err := operator.AddHost(ctx, list.Revision, entry)
	if err != nil {
		return "", err
	}
	if change.Outcome != iwire.EditOutcomeApplied {
		return "", fmt.Errorf("demonstration model server registration: %s", change.Outcome.String())
	}
	return registerReviewExample(ctx, v)
}

func registerReviewExample(ctx context.Context, v reviewExamples) (string, error) {
	apps, err := (&servicePanel{}).nativeApplicationsForPanel(ctx, "pretend-lemonade")
	if err != nil {
		return "Demonstration app registration is pending: OpenAbstractions cannot read apps right now.", nil
	}
	result, err := apps.Register(ctx, fwire.ApplicationDescriptor{Name: "pretend-lemonade", Program: identity.CanonicalProgramPath(v.Program)})
	if err != nil {
		return "Demonstration app registration is pending: the native application owner could not verify the example image.", nil
	}
	if result.Outcome != fwire.ApplicationOutcomeApplied {
		return fmt.Sprintf("Demonstration app registration is pending: native registration %s (%s).", result.Outcome.String(), result.Reason), nil
	}
	return "", nil
}
