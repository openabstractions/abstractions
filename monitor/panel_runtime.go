package main

import (
	"errors"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"

	facade "github.com/openabstractions/abstraction-facade/go"
	client "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// explicitRuntime is a runtime the person named on the command line instead of
// the installed one: its resolver endpoint and the program and account it must
// run as. The Panel binds it with the same server trust installed selection
// supplies, and never falls back to the installed runtime.
type explicitRuntime struct {
	Endpoint string `json:"endpoint"`
	Program  string `json:"program"`
	Account  string `json:"account"`
	// StateDir is the runtime's own --state-dir, where Setup keeps its state
	// for that runtime; empty when the person did not name it, and Setup then
	// refuses.
	StateDir string `json:"stateDir,omitempty"`
}

// errSetupNeedsStateDir is Setup's refusal for a named runtime whose state
// folder the Panel was not told.
var errSetupNeedsStateDir = errors.New("Setup keeps its state in the served runtime's state folder; start the Panel with -runtime-state-dir set to that runtime's --state-dir")

// panelExplicit is nil when the Panel uses the installed runtime.
var panelExplicit *explicitRuntime

// currentPrincipal is this process's own account, as the runtime's listener
// binds it for a runtime started by the same person.
func currentPrincipal() (identity.User, string, error) {
	account, err := user.Current()
	if err != nil {
		return identity.User{}, "", err
	}
	if runtime.GOOS == "windows" {
		return identity.User{Kind: "windows", SID: account.Uid, UID: -1, GID: -1}, account.Uid, nil
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return identity.User{}, "", err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return identity.User{}, "", err
	}
	return identity.User{Kind: "posix", UID: uid, GID: gid}, account.Uid, nil
}

// bindExplicitRuntime makes every Panel call resolve through the named runtime,
// verified as program under this process's account. program is stored and
// compared as its canonical long path (identity.CanonicalProgramPath), so a
// short DOS 8.3 launch alias names the same runtime as its long spelling.
// Setup's state follows the runtime (live walkthrough 2026-10-02, D6): it
// lives under stateDir, that runtime's --state-dir, and without stateDir
// Setup refuses instead of writing the person's own Setup state.
func bindExplicitRuntime(endpoint, program, stateDir string) error {
	if endpoint == "" || program == "" {
		return errors.New("-runtime-endpoint and -runtime-program are required together")
	}
	if !filepath.IsAbs(program) {
		return errors.New("-runtime-program must be the absolute path of the runtime executable")
	}
	if stateDir != "" && !filepath.IsAbs(stateDir) {
		return errors.New("-runtime-state-dir must be the absolute --state-dir the runtime was started with")
	}
	principal, account, err := currentPrincipal()
	if err != nil {
		return err
	}
	if stateDir == "" {
		setup.UseNoState(errSetupNeedsStateDir)
	} else {
		stateDir = filepath.Clean(stateDir)
		if err := setup.UseRuntimeState(stateDir); err != nil {
			return err
		}
	}
	program = identity.CanonicalProgramPath(filepath.Clean(program))
	machine := client.NewVerified(endpoint, listen.ServerExpectation{Principal: principal, Program: program})
	panelMachine = func() *facade.Machine { return machine }
	panelExplicit = &explicitRuntime{Endpoint: endpoint, Program: program, Account: account, StateDir: stateDir}
	return nil
}
