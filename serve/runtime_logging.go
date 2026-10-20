package main

import (
	"errors"
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	"os"
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
	emitter, err := runtimeLogEmitter()
	if err != nil {
		return nil, err
	}
	return logging.WithEmitter(provider, emitter)
}

func runtimeLogEmitter() (logging.Emitter, error) {
	principal, err := identity.Self()
	if err != nil {
		return logging.Emitter{}, err
	}
	account, err := principal.Account()
	if err != nil {
		return logging.Emitter{}, errors.New("runtime: native self log account unavailable")
	}
	image, err := os.Executable()
	if err != nil {
		return logging.Emitter{}, err
	}
	resource, err := logging.PrepareApplicationResource(image)
	if err != nil {
		return logging.Emitter{}, err
	}
	return logging.Emitter{Version: 1, Account: account, Program: resource.Program, Key: resource.CapturedFileKey}, nil
}
