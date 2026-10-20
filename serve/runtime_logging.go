package main

import (
	"errors"
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	"os"
	"strconv"
)

// runtimeAttributedLogWriter is only for trusted self-generated events. The
// raw provider remains the receiving service's sink and retention consumer.
func runtimeAttributedLogWriter(out logging.Sink) (logging.Sink, error) {
	if out == nil {
		return nil, nil
	}
	provider, ok := out.(*logging.FileSink)
	if !ok || !provider.ApplicationHistoryAvailable() {
		return out, nil
	}
	principal, err := identity.Self()
	if err != nil {
		return nil, err
	}
	account := ""
	if principal.Kind == "windows" {
		account = principal.SID
	} else if principal.Kind == "posix" {
		account = strconv.Itoa(principal.UID)
	}
	if account == "" {
		return nil, errors.New("runtime: native self log account unavailable")
	}
	image, err := os.Executable()
	if err != nil {
		return nil, err
	}
	resource, err := logging.PrepareApplicationResource(image)
	if err != nil {
		return nil, err
	}
	return logging.WithEmitter(provider, logging.Emitter{Version: 1, Account: account, Program: resource.Program, Key: resource.CapturedFileKey})
}
