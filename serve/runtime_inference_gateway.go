package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"

	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-inference/go/gateway"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// gatewayAddress accepts an empty value (the window stays closed) or
// 127.0.0.1:<port>. The window never listens on another address.
func gatewayAddress(address string) error {
	if address == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(address)
	if n, perr := strconv.Atoi(port); err != nil || host != "127.0.0.1" || perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("runtime: --gateway %q must be 127.0.0.1:<port>; the inference gateway window listens on IPv4 loopback only", address)
	}
	return nil
}

// openGateway opens the window over the composed provider. Keys are verified
// against the holder, decisions and records are the provider's own, and a
// window refusal before admission is recorded through record.
func openGateway(address string, r *runtimeInference, record func(inference.Record), report func(error)) (*gateway.Window, error) {
	o := r.operator
	window, err := gateway.Listen(address, gateway.Config{Chat: r.provider, Account: o.credentials.owner, Keys: o.verifyKey, StoreContent: r.content.write, ReadContent: r.content.read,
		Models: o.windowModels, Record: record, OnError: report})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "runtime: inference gateway window on http://%s/v1 (OpenAI) and http://%s (Anthropic)\n", window.Addr(), window.Addr())
	return window, nil
}

// verifyKey resolves a presented key to its record, verifies it against the
// platform store, and requires the bound program the key was issued to.
func (o *inferenceOperator) verifyKey(_ context.Context, subject inference.Subject, key string) (gateway.Grant, string) {
	name, ok := localKeyName(key)
	if !ok {
		return gateway.Grant{}, gateway.KeyUnknown
	}
	o.mu.Lock()
	record, found := o.keys.byName(name)
	o.mu.Unlock()
	if !found {
		return gateway.Grant{}, gateway.KeyUnknown
	}
	switch o.credentials.holder.VerifyLocalKey(o.credentials.owner, name, []byte(key)) {
	case "verified":
	case "revoked", "expired", "lost":
		return gateway.Grant{}, gateway.KeyRevoked
	case "unavailable":
		return gateway.Grant{}, gateway.KeyUnavailable
	default:
		return gateway.Grant{}, gateway.KeyUnknown
	}
	if subject.Account != o.credentials.owner || !samePrograms(record.Program, subject.Program) {
		return gateway.Grant{}, gateway.KeyWrongProgram
	}
	return gateway.Grant{Credential: record.Credential}, gateway.KeyVerified
}

// windowModels lists every model name a host serves on which subject holds
// abstraction.inference/complete, from the router's latest reading.
func (o *inferenceOperator) windowModels(ctx context.Context, subject inference.Subject) []string {
	families, _ := o.router.Models(false)
	permitted := map[string]bool{}
	seen := map[string]bool{}
	var names []string
	for _, family := range families {
		for _, alias := range family.Names {
			if !alias.Servable || seen[alias.Name] {
				continue
			}
			allowed, asked := permitted[alias.Host]
			if !asked {
				decision := o.credentials.runtimeRights.decide(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, inference.ActionComplete, inference.ResourceHost(alias.Host))
				allowed = decision.Outcome == rwire.DecisionOutcomePermitted
				permitted[alias.Host] = allowed
			}
			if allowed {
				seen[alias.Name] = true
				names = append(names, alias.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// Files and defaults of the persisted gateway window setting.
const (
	// inferenceGatewayFile keeps the setting beside hosts.json in
	// <state>/inference. An absent file keeps the window closed.
	inferenceGatewayFile = "gateway.json"
	// defaultGatewayAddress is where the window opens when the setting names
	// no address.
	defaultGatewayAddress = "127.0.0.1:8793"
)

// gatewaySetting is the persisted window setting. The installed hosts start
// serve runtime with fixed arguments, and this file is how an installed
// runtime opens the window.
type gatewaySetting struct {
	Open    bool   `json:"open"`
	Address string `json:"address,omitempty"`
}

// gatewayControl opens and closes the window of a running runtime. It owns the
// window's listener; the runtime stops it when the inference service stops.
type gatewayControl struct {
	mu      sync.Mutex
	path    string
	open    func(address string) (*gateway.Window, error)
	report  func(error)
	window  *gateway.Window
	served  chan error
	why     string
	stopped bool
}

// readSetting reads the setting and its revision, the digest of its bytes.
func (g *gatewayControl) readSetting() (gatewaySetting, string, error) {
	raw, err := os.ReadFile(g.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return gatewaySetting{}, "", err
	}
	sum := sha256.Sum256(raw)
	revision := "gateway-v1:" + hex.EncodeToString(sum[:12])
	var setting gatewaySetting
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &setting); err != nil {
			return gatewaySetting{}, revision, fmt.Errorf("inference: %s: %w", g.path, err)
		}
		if err := gatewayAddress(setting.Address); err != nil {
			return gatewaySetting{}, revision, fmt.Errorf("inference: %s: %w", g.path, err)
		}
	}
	return setting, revision, nil
}

// apply brings the window to open at address, or closed. Callers hold g.mu.
func (g *gatewayControl) apply(open bool, address string) error {
	if g.window != nil && (!open || g.window.Addr().String() != address) {
		g.closeWindow()
	}
	g.why = ""
	if !open || g.window != nil {
		return nil
	}
	if g.stopped {
		return errors.New("the inference service has stopped")
	}
	window, err := g.open(address)
	if err != nil {
		g.why = "listen:" + err.Error()
		return err
	}
	g.window, g.served = window, make(chan error, 1)
	go func(served chan error) { served <- window.Serve(context.Background()) }(g.served)
	return nil
}

// closeWindow closes the listener and every window connection, and waits for
// the window's Serve to return. Callers hold g.mu.
func (g *gatewayControl) closeWindow() {
	if g.window == nil {
		return
	}
	if err := g.window.Close(); err != nil {
		g.report(err)
	}
	if err := <-g.served; err != nil {
		g.report(err)
	}
	g.window, g.served = nil, nil
}

// start opens the window from the --gateway flag, which leaves the setting
// unchanged and fails composition when it cannot listen, or else from the
// setting, whose failure is reported and read back through Gateway.
func (g *gatewayControl) start(flag string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if flag != "" {
		return g.apply(true, flag)
	}
	setting, _, err := g.readSetting()
	if err != nil {
		g.why = "setting:" + err.Error()
		g.report(err)
		return nil
	}
	if !setting.Open {
		return nil
	}
	address := setting.Address
	if address == "" {
		address = defaultGatewayAddress
	}
	if err := g.apply(true, address); err != nil {
		g.report(fmt.Errorf("runtime: inference gateway window: %w", err))
	}
	return nil
}

// stop closes the window for good.
func (g *gatewayControl) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = true
	g.closeWindow()
}

// Gateway reads the window setting and whether the window listens.
func (o *inferenceOperator) Gateway(ctx context.Context, caller inference.Subject) iwire.GatewayState {
	refused := func(outcome iwire.ListOutcome) iwire.GatewayState { return iwire.GatewayState{Outcome: outcome} }
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		return refused(inferenceListRefusal(word))
	}
	g := o.gateway
	if g == nil {
		return refused(iwire.ListOutcomeUnavailable)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	setting, revision, err := g.readSetting()
	if err != nil {
		o.report(err)
		return refused(iwire.ListOutcomeUnavailable)
	}
	state := iwire.GatewayState{Outcome: iwire.ListOutcomePage, Revision: revision, Open: setting.Open, Address: setting.Address, Why: g.why}
	if g.window != nil {
		state.Listening, state.ListeningAddress = true, g.window.Addr().String()
	}
	return state
}

// SetGateway writes the window setting at its revision and applies it to the
// running runtime before replying.
func (o *inferenceOperator) SetGateway(ctx context.Context, caller inference.Subject, expected string, open bool, address string) iwire.GatewayChange {
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		return iwire.GatewayChange{Outcome: inferenceEditRefusal(word)}
	}
	if gatewayAddress(address) != nil {
		return iwire.GatewayChange{Outcome: iwire.EditOutcomeInvalid, Reason: "address"}
	}
	g := o.gateway
	if g == nil {
		return iwire.GatewayChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// An unreadable setting is replaced at its listed revision.
	setting, revision, err := g.readSetting()
	if err != nil {
		o.report(err)
	}
	if expected != revision {
		return iwire.GatewayChange{Outcome: iwire.EditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	if address == "" {
		address = setting.Address
	}
	if address == "" && open {
		address = defaultGatewayAddress
	}
	if err := writeAtomically(g.path, gatewaySetting{Open: open, Address: address}); err != nil {
		o.report(err)
		return iwire.GatewayChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	if _, revision, err = g.readSetting(); err != nil {
		o.report(err)
		return iwire.GatewayChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	if err := g.apply(open, address); err != nil {
		o.report(fmt.Errorf("runtime: inference gateway window: %w", err))
		return iwire.GatewayChange{Outcome: iwire.EditOutcomeUnavailable, Reason: g.why}
	}
	return iwire.GatewayChange{Outcome: iwire.EditOutcomeApplied, Revision: revision}
}
