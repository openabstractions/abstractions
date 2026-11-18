package main

import (
	"context"
	"log"
	"runtime"
	"slices"
	"time"

	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	facade "github.com/openabstractions/abstraction-facade/go"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// stepVideoSet is the video model set an app's request for video sets up.
var stepVideoSet = modelhost.LTX25.Name

// huggingFaceHost and downloadConsumer scope the credential the gated
// Lightricks files are fetched with.
const (
	huggingFaceHost  = "huggingface.co"
	downloadConsumer = "abstraction.download/http-execution@1"
)

// stepVideo is the step's video part: the reviewed set on this platform's
// pinned engine, nil where either has no review.
func (p *servicePanel) stepVideo() *setup.StepVideo {
	set, ok := modelhost.VideoModelSetNamed(stepVideoSet)
	if !ok {
		return nil
	}
	engine, ok := modelhost.PinnedVideoEngine(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return nil
	}
	return &setup.StepVideo{Set: set, Engine: engine,
		Served: func(ctx context.Context) (bool, error) { return stepVideoRouted(ctx, set.Name, true) },
		Get: func(ctx context.Context) error {
			err := p.modelHostDeclare(ctx, stepVideoArgs(set.Name, stepVideoCredential(ctx, listCredentials))...)
			if err == nil {
				if fill := setupFillVideoDefault(ctx); fill != nil {
					log.Printf("setup video: the video default: %v", fill)
				}
			}
			return err
		}}
}

// stepVideoArgs is the command that gets a video set ready:
// openabstractions models host --video-model SET --get, with the person's
// Hugging Face credential when one is registered.
func stepVideoArgs(set, credential string) []string {
	args := []string{"models", "host", "--video-model", set, "--get"}
	if credential != "" {
		args = append(args, "--credential", credential)
	}
	return args
}

// stepVideoRouted reports whether the runtime's router serves model for
// video on this computer: a host here holds it or has it installed.
func stepVideoRouted(ctx context.Context, model string, fresh bool) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	router, err := panelMachine().ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return false, err
	}
	pick, err := router.PickContext(ctx, routerclient.PickRequest{Model: model, Profile: "video", Fresh: fresh})
	if err != nil {
		return false, err
	}
	return slices.Contains([]string{"resident", "would-load"}, pick.Decision.Verdict), nil
}

// listCredentials reads one page of the account's credential names.
func listCredentials(ctx context.Context, cursor string) (cwire.MetadataPage, error) {
	holder, err := panelMachine().ResolveCredentials(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return cwire.MetadataPage{}, err
	}
	return holder.List(ctx, cursor, 32)
}

// stepVideoCredential is the name of the person's active credential for
// Hugging Face downloads, the first by name; "" when none is registered or
// the list cannot be read (display data: the download then runs without
// one and a gated file stops it).
func stepVideoCredential(ctx context.Context, list func(context.Context, string) (cwire.MetadataPage, error)) string {
	var names []string
	cursor := ""
	for range 16 {
		page, err := list(ctx, cursor)
		if err != nil || page.Outcome != cwire.PageOutcomePage {
			log.Printf("setup video: the credential list: %v %v", err, page.Outcome)
			break
		}
		for _, r := range page.Records {
			if r.State == cwire.StateActive && slices.Contains(r.Scope.Targets, huggingFaceHost) && slices.Contains(r.Scope.Consumers, downloadConsumer) {
				names = append(names, r.Name)
			}
		}
		if page.Complete || page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(names) == 0 {
		return ""
	}
	return slices.Min(names)
}
