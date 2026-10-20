// Command openabstractions-system is OpenAbstractions' protected-operation
// helper (abstraction-facade CONTRACT.md FAC-H1): the one program OA raises
// the platform's administrator prompt for, after the person's yes in OA's own
// prompt. It accepts exactly `<operation> --ticket <ticket>`, checks that it
// runs elevated from the machine-wide installation, verifies the runtime it
// talks to (identity ID-H2), claims the frozen request, performs that one act
// and reports its result. It writes nothing to disk and keeps no state.
//
// Exit status: 0 completed, 2 refused command line, 3 refused by the
// runtime, 4 the act failed, 5 not running elevated (or not on Windows),
// 6 not the machine-wide installation's copy. A windowless elevated image
// shows no output, so the status is its only report.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/trustedpath"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-facade/go-core/protected"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
)

const (
	exitDone         = 0
	exitUsage        = 2
	exitRefused      = 3
	exitActError     = 4
	exitNotElevated  = 5
	exitNotInstalled = 6
)

// highIntegrity is SECURITY_MANDATORY_HIGH_RID, an elevated token's level.
const highIntegrity = 0x3000

// acts is the compiled catalogue: each operation's act, supplied by the
// capability that owns it.
var acts = map[string]protected.Act{
	protected.OperationSecurityLogPage: logservice.RunProtectedSourceRead,
}

// helperEnv is everything the helper reads from its machine, replaced by
// tests.
type helperEnv struct {
	goos     string
	self     func() (identity.User, error)
	image    func() (string, error)
	trusted  func(string) error
	servers  func(helper string, self identity.User) ([]listen.ServerExpectation, error)
	endpoint func(string) (string, error)
	run      func(context.Context, string, []listen.ServerExpectation, string, string, protected.Act) error
}

func systemEnv() helperEnv {
	return helperEnv{
		goos:  runtime.GOOS,
		self:  identity.Self,
		image: protected.OwnImage,
		trusted: func(path string) error {
			return trustedpath.CheckExecutable(path, trustedpath.MachineOwners())
		},
		servers:  protected.ServerExpectations,
		endpoint: bootstrap.Endpoint,
		run:      protected.RunHelper,
	}
}

func main() {
	if err := restrictLibrarySearch(); err != nil {
		fmt.Fprintf(os.Stderr, "openabstractions-system: refused: library search not restricted: %v\n", err)
		os.Exit(exitNotInstalled)
	}
	os.Exit(run(os.Args[1:], os.Stderr, systemEnv()))
}

func run(args []string, stderr io.Writer, env helperEnv) int {
	refuse := func(code int, format string, a ...any) int {
		fmt.Fprintf(stderr, "openabstractions-system: "+format+"\n", a...)
		return code
	}
	operation, ticket, err := protected.ParseHelperArguments(args)
	if err != nil {
		return refuse(exitUsage, "%v", err)
	}
	act := acts[operation]
	if act == nil {
		return refuse(exitUsage, "no act for %s", operation)
	}
	if env.goos != "windows" {
		return refuse(exitNotElevated, "protected operations exist only on Windows in this release")
	}
	self, err := env.self()
	if err != nil {
		return refuse(exitNotElevated, "own token unreadable: %v", err)
	}
	if self.Kind != "windows" || !self.Elevated || self.IntegrityRID < highIntegrity {
		return refuse(exitNotElevated, "refused: not running elevated; OpenAbstractions starts this program through the administrator prompt")
	}
	image, err := env.image()
	if err != nil {
		return refuse(exitNotInstalled, "own image unreadable: %v", err)
	}
	if err := env.trusted(image); err != nil {
		return refuse(exitNotInstalled, "refused: this copy is not the machine-wide installation's: %v", err)
	}
	servers, err := env.servers(image, self)
	if err != nil {
		return refuse(exitNotInstalled, "%v", err)
	}
	endpoint, err := env.endpoint(protected.EndpointService)
	if err != nil {
		return refuse(exitRefused, "runtime endpoint: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), protected.HelperDeadline)
	defer cancel()
	err = env.run(ctx, endpoint, servers, operation, ticket, act)
	switch {
	case err == nil:
		return exitDone
	case errors.Is(err, protected.ErrRefused), errors.Is(err, listen.ErrServerUntrusted):
		return refuse(exitRefused, "%v", err)
	}
	return refuse(exitActError, "%v", err)
}
