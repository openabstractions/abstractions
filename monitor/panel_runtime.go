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
)

// explicitRuntime is a runtime the person named on the command line instead of
// the installed one: its resolver endpoint and the program and account it must
// run as. The Panel binds it with the same server trust installed selection
// supplies, and never falls back to the installed runtime.
type explicitRuntime struct {
	Endpoint string `json:"endpoint"`
	Program  string `json:"program"`
	Account  string `json:"account"`
}

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
// verified as program under this process's account.
func bindExplicitRuntime(endpoint, program string) error {
	if endpoint == "" || program == "" {
		return errors.New("-runtime-endpoint and -runtime-program are required together")
	}
	if !filepath.IsAbs(program) {
		return errors.New("-runtime-program must be the absolute path of the runtime executable")
	}
	principal, account, err := currentPrincipal()
	if err != nil {
		return err
	}
	machine := client.NewVerified(endpoint, listen.ServerExpectation{Principal: principal, Program: filepath.Clean(program)})
	panelMachine = func() *facade.Machine { return machine }
	panelExplicit = &explicitRuntime{Endpoint: endpoint, Program: filepath.Clean(program), Account: account}
	return nil
}
