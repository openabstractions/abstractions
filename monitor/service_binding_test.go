package main

import (
	"os"
	"os/user"
	"runtime"
	"strconv"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go"
	client "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// The fixture server runs inside this test executable. Its expected account and
// image come from local process state, never from a resolver/provider response.
// Tests using this package-wide factory must not run in parallel.
func trustPanelRuntime(t *testing.T, endpoint string) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("verified local fixture requires Program proof unavailable on macOS")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.User{Kind: "windows", SID: account.Uid, UID: -1, GID: -1}
	if runtime.GOOS != "windows" {
		uid, err := strconv.Atoi(account.Uid)
		if err != nil {
			t.Fatal(err)
		}
		gid, err := strconv.Atoi(account.Gid)
		if err != nil {
			t.Fatal(err)
		}
		principal = identity.User{Kind: "posix", UID: uid, GID: gid}
	}
	server := listen.ServerExpectation{Principal: principal, Program: exe}
	previous := panelMachine
	panelMachine = func() *facade.Machine { return client.NewVerified(endpoint, server) }
	t.Cleanup(func() { panelMachine = previous })
}
