package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// runtimeApplicationsEndpoint is the one name of the applications listener.
func runtimeApplicationsEndpoint(options runtimeFlags) (string, error) {
	return options.companionEndpoint("applications-v1", "", "applications")
}

func composeApplications(options runtimeFlags, policy *runtimeRights, report func(error)) (*applicationsHost, string, error) {
	if policy == nil {
		return nil, "", errors.New("applications: rights policy unavailable")
	}
	state, err := credentialsState(options)
	if err != nil {
		return nil, "", err
	}
	endpoint, err := runtimeApplicationsEndpoint(options)
	if err != nil {
		return nil, "", err
	}
	directory, err := openApplications(state, policy.owner, func(ctx context.Context, s rights.Subject, action, resource string) (bool, error) {
		decision := policy.decide(ctx, s, action, resource)
		if decision.Outcome == rights.DecisionOutcomeUnavailable {
			return false, errors.New("applications: policy unavailable")
		}
		return decision.Outcome == rights.DecisionOutcomePermitted, nil
	})
	if err != nil {
		return nil, "", err
	}
	// Only the operator programs read the start log (APPLICATIONS.md).
	directory.operator = policy.isOperator
	// Only the Panel records a person's Connect click (ExpectConnect).
	directory.connectPanel = policy.isPanel
	directory.connectPeer = policy.isPanelPeer
	directory.connectReport = report
	directory.programConfigKeep = policy.keepProviderConfig
	directory.programConfigRemove = policy.removeProviderConfig
	directory.programConfigReconcile = policy.retireProviderConfig
	directory.programConfigReport = report
	directory.programHoldKeep = policy.permitKeptHolds
	directory.programHoldRetire = policy.retireKeptHolds
	directory.agentExcluded = applicationAgentGatewayExclusion
	if options.isolated != "" {
		// An isolated runtime is not the installed one, so the installed
		// lookup, which requires that, would refuse every enrollment
		// (A-JRN2). It still excludes the installed shared gateway image when
		// one is installed (backlog/JOURNEY-SCOPE.md 1). The person's Keep
		// question still decides.
		directory.agentExcluded = isolatedAgentGatewayExclusion
	}
	// Owner, 2026-10-31 ("attach to all sessions") and 2026-11-09 (dormant
	// chats must be reachable): a question wakes the receiver's own existing
	// chat through its registered recipe. Starts stay limited to retained
	// chats; a question never starts a new session. Registration and the
	// standing message permission grant no start: the person's per-direction
	// start permit, the Agent starts settings and the hourly limit decide.
	directory.messageStartProfile = messageStartProfileResolver(approveMessageStartProfile)
	directory.messageStartChatsOnly = true
	directory.messageCodexRunning = nativecli.CodexDesktopRunning
	if err := policy.configureMessagePermits(directory); err != nil {
		return nil, "", err
	}
	// A program that registered itself is listed and started by the
	// operator programs (APPLICATIONS.md): the runtime writes read and
	// activate on its entry for each operator with no rule there, a Block
	// included, and reports a failure without undoing the registration.
	directory.registeredSelf = func(name string) {
		resource := "app:" + name
		rules := []installationRule{{ActionApplicationRead, resource}, {ActionApplicationActivate, resource}}
		if err := policy.permitOperators(rules, registeredSelfWhy); err != nil && report != nil {
			report(err)
		}
	}
	listener, err := listenApplications(endpoint, directory, policy.owner, report)
	if err == nil {
		directory.launchMessageStart = newOwnedMessageStartLauncher(directory, listener.ctx)
		listener.messages, err = openMessageStore(state, directory.now)
		listener.messageRights = policy
		if err != nil {
			listener.Close()
			return nil, "", err
		}
		listener.stopMessageRecheck = listener.followMessageChanges(listener.ctx)
		composeMessageDiscovery(directory, listener.messages, func(ctx context.Context, application string) bool {
			settings, err := listener.readMessageStartSettings(ctx, application)
			return err == nil && settings.Receiver
		})
	}
	return listener, endpoint, err
}

// composeMessageDiscovery makes the registered launchers' read-only listing
// the production discovery of existing conversations (architect ruling 1,
// AB-1s), named by the store's one chat-* name function.
func composeMessageDiscovery(directory *applicationDirectory, store *messageStore, enabled func(context.Context, string) bool) {
	name := func(program, chat string) string { return store.chatContextName(directory.owner, program, chat) }
	discovery := newMessageDiscovery(directory.fetchAgentSessions(name, enabled), directory.now)
	directory.messageAttachSessions = func(ctx context.Context, application string) ([]applicationAttachSession, error) {
		return discovery.sessions(ctx, application, false), nil
	}
	directory.messageAttachRefresh = func(ctx context.Context, application string) []applicationAttachSession {
		return discovery.sessions(ctx, application, true)
	}
	directory.messageAttachWarm = discovery.warm
	directory.messageAttachCurrent = discovery.current
}
func configureApplications(o *host.Options, h *applicationsHost, endpoint string) {
	if h == nil {
		return
	}
	o.Applications = h
	o.ApplicationsEndpoint = endpoint
	o.RightsActions = append(o.RightsActions, ActionApplicationManage, ActionApplicationRead, ActionApplicationAnnounce, ActionApplicationActivate, ActionApplicationRegisterSelf)
	if h.messages != nil {
		o.RightsActions = append(o.RightsActions, ActionApplicationMessage, grants.ApplicationMessageStartAction)
	}
	if h.v3 != nil {
		o.RightsActions = append(o.RightsActions, appmodel.CommandActions...)
	}
}

// Isolated runtimes never change installed login integration. The installed
// runtime observes its own Applications service, including the initial state;
// it does not derive a count from Panel, provider declarations or live presence.
func startApplicationsLogon(ctx context.Context, options runtimeFlags, h *applicationsHost, apply runtimeLogonApply, report func(error)) func() {
	if options.isolated != "" || h == nil {
		return func() {}
	}
	h.signIn.bind(readRuntimeLogon)
	return watchRuntimeLogon(ctx, h.directory.loginState, apply, report)
}

// registeredSelfWhy is the reason on the operator rules a self-registration
// writes.
const registeredSelfWhy = "registered by the app"

// installedAgentGatewayKey finds the installed shared gateway image; tests
// replace it.
var installedAgentGatewayKey = installedSharedGatewayKey

// isolatedAgentGatewayExclusion excludes the installed shared gateway image
// when one is installed and nothing when none is.
func isolatedAgentGatewayExclusion(ctx context.Context, _ string, key string) (bool, error) {
	installed, found, err := installedAgentGatewayKey(ctx)
	if err != nil || !found {
		return false, err
	}
	return key == installed, ctx.Err()
}
