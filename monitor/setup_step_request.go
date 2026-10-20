package main

import (
	"context"
	"errors"
	"log"
	"os/exec"
	"strings"
	"time"
	"unicode"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The presenter's half of an app's setup request (applications@3
// RequestSetup and ReceiveSetupRequest; HP5 slice 2). The runtime keeps the one
// pending request; the presenter reads it, starts --setup for it and names it
// in its next read, which clears it. The step's one-per-session claim
// (claimSetupStep) serializes the work: a second step started while one is
// open exits at once.

// setupFinishFlags keeps the installer's private upgrade flag on Finish.
func setupFinishFlags(finish bool, names []string, positional int) error {
	for _, name := range names {
		if name == "update" && !finish {
			return errors.New("-update requires -finish")
		}
		if finish && name != "finish" && name != "update" && name != "native" && name != "quiet" {
			return errors.New("-finish takes only -update, -native and -quiet")
		}
	}
	if finish && positional != 0 {
		return errors.New("-finish takes no positional arguments")
	}
	return nil
}

// stepRequesterFlags reads -setup-use and -setup-app: both or neither.
func stepRequesterFlags(use, app, request string) (*setup.StepRequester, error) {
	if use == "" && app == "" && request == "" {
		return nil, nil
	}
	if use != setup.UseCode && use != setup.UseImage && use != setup.UseVideo {
		return nil, errors.New("-setup-use takes code, image or video")
	}
	if app == "" || len(app) > 128 || strings.IndexFunc(app, unicode.IsControl) >= 0 {
		return nil, errors.New("-setup-app takes the requesting app's display name")
	}
	if request == "" || len(request) > 64 || strings.IndexFunc(request, unicode.IsControl) >= 0 {
		return nil, errors.New("-setup-request takes the runtime's request id")
	}
	return &setup.StepRequester{App: app, Use: use, Request: request}, nil
}

// launchSetupStepFor starts the Panel's --setup mode for one request,
// detached from the presenter.
func launchSetupStepFor(request applications.SetupRequest) error {
	if _, err := stepRequesterFlags(request.Use, request.Application, request.ID); err != nil {
		return err
	}
	self, err := panelExecutable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "--setup", "--setup-use", request.Use, "--setup-app", request.Application, "--setup-request", request.ID)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// setupRequestHold is how long the runtime holds one ReceiveSetupRequest
// open while no request is pending (serve setupReadWait). The presenter's
// call budget (panelApplicationsCallBudget) outlasts it; the transport's
// default budget of 5 s cut every read short.
const setupRequestHold = 20 * time.Second

// receiveSetupRequest is the presenter's applications@3 ReceiveSetupRequest.
func receiveSetupRequest(ctx context.Context, after string) (applications.SetupRequestRead, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return applications.SetupRequestRead{}, err
	}
	return applications.NewApplicationsClient(panelApplicationsTransport(binding.Transport()).WithContext(ctx)).ReceiveSetupRequest(after)
}

func exchangeSetupStep(ctx context.Context, receipt applications.SetupPresenterReceipt) (applications.SetupStepReport, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return applications.SetupStepReport{}, err
	}
	return applications.NewApplicationsClient(panelApplicationsTransport(binding.Transport()).WithContext(ctx)).ReportSetupStep(receipt)
}

// watchSetupRequests reads the pending request until ctx ends and starts the
// step for each new one. A refused or failed read waits before the next.
func watchSetupRequests(ctx context.Context, read func(context.Context, string) (applications.SetupRequestRead, error), launch func(applications.SetupRequest) error, pause func(context.Context) error) {
	after := ""
	for ctx.Err() == nil {
		result, err := read(ctx, after)
		switch {
		case err == nil && result.Outcome == applications.OutcomePage && result.Request != nil:
			if result.Request.ID == after {
				continue
			}
			if err := launch(*result.Request); err != nil {
				log.Printf("start setup for an app's request: %v", err)
			}
			// The next read names it, which clears it in the runtime.
			after = result.Request.ID
			continue
		case err == nil && result.Outcome == applications.OutcomePage:
			continue
		case err != nil:
			log.Printf("read setup requests: %v", err)
		default:
			log.Printf("read setup requests: %s %s", result.Outcome, result.Reason)
		}
		if pause(ctx) != nil {
			return
		}
	}
}

// setupRequestPause waits before the next read after a refusal or failure.
func setupRequestPause(ctx context.Context) error {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// startSetupRequestWatch runs watchSetupRequests for the background
// presenter.
func startSetupRequestWatch(ctx context.Context) {
	go watchSetupRequests(ctx, receiveSetupRequest, launchSetupStepFor, setupRequestPause)
}
