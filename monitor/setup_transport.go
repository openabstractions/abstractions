package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type setupTransportMode struct {
	Native, NativeExplicit, Open      bool
	Page, Endpoint, Program, StateDir string
	Flags                             []string
	Args                              int
}

func validateSetupTransportMode(path string, mode setupTransportMode) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) || mode.Native || !mode.NativeExplicit || mode.Open || mode.Page != "setup" || mode.Args != 0 {
		return errors.New("-setup-transport requires an absolute startup file and -native=false -open=false -page setup")
	}
	if mode.Endpoint != "" || mode.Program != "" || mode.StateDir != "" {
		if mode.Endpoint == "" || !filepath.IsAbs(mode.Program) || !filepath.IsAbs(mode.StateDir) {
			return errors.New("-setup-transport requires a complete explicit runtime selection")
		}
	}
	explicit := mode.Endpoint != ""
	allowed := map[string]bool{"setup-transport": true, "native": true, "open": true, "page": true, "addr": true, "quiet": true, "runtime-endpoint": true, "runtime-program": true, "runtime-state-dir": true}
	for _, name := range mode.Flags {
		if !allowed[name] {
			return errors.New("-setup-transport cannot be combined with another Panel mode or action")
		}
		if !explicit && (name == "runtime-endpoint" || name == "runtime-program" || name == "runtime-state-dir") {
			return errors.New("installed Setup qualification requires the default runtime selection")
		}
	}
	return nil
}

func setupTransportForPanel(input setup.QualificationInput, explicit *explicitRuntime, machine *client.Machine) (*setup.PublicTransfer, error) {
	if explicit == nil || machine == nil || input.RuntimeEndpoint != explicit.Endpoint || input.RuntimeProgram != explicit.Program || input.RuntimeStateDir != explicit.StateDir {
		return nil, errors.New("startup transport differs from the selected verified runtime")
	}
	transfer, err := setup.NewQualificationTransfer(machine, input.Mirrors, input.Expires)
	if err != nil {
		return nil, err
	}
	return &transfer, nil
}

// readSetupTransport reads untrusted transport DATA once. File identity and
// ordinary-file checks bound this read; they grant no requester authority.
func readSetupTransport(path string) (setup.QualificationInput, error) {
	var input setup.QualificationInput
	handle, err := fileid.Open(context.Background(), fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return input, err
	}
	info, err := handle.File().Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > setup.QualificationInputMaxBytes {
		return input, errors.Join(errors.New("startup transport must be a bounded ordinary file"), err, handle.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(handle.File(), setup.QualificationInputMaxBytes+1))
	if err := errors.Join(readErr, handle.Close()); err != nil {
		return input, err
	}
	return setup.ParseQualificationInput(data)
}
