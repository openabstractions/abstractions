package setup

import (
	"bytes"
	"errors"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/redact"
)

const maxProviderReply = 16 << 10

type providerExit struct {
	Code   int
	Reason string
	Engine string
}

func (e *providerExit) Error() string {
	return "installed " + e.Engine + " setup failed: " + e.Reason
}

func providerExitCode(err error) int {
	var exit *providerExit
	if errors.As(err, &exit) {
		return exit.Code
	}
	return 0
}

// providerFailure redacts output before Setup presents a provider failure.
func providerFailure(engine, stderr string, err error) *providerExit {
	reason := redact.Line(stderr, 0)
	if reason == "" {
		reason = err.Error()
	}
	code := 1
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		code = exited.ExitCode()
	}
	return &providerExit{Code: code, Reason: reason, Engine: engine}
}

type cappedReply struct{ data bytes.Buffer }

func (reply *cappedReply) Write(data []byte) (int, error) {
	if len(data) > maxProviderReply-reply.data.Len() {
		return 0, errors.New("installed engine output exceeded its limit")
	}
	return reply.data.Write(data)
}

func (reply *cappedReply) Bytes() []byte  { return reply.data.Bytes() }
func (reply *cappedReply) String() string { return reply.data.String() }
