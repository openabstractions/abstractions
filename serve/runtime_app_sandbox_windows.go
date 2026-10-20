//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	"golang.org/x/sys/windows"
)

// installedOpenCode is the official installation the launch named
// (--program, --plugin, --config) with the identities the session checks.
type installedOpenCode struct {
	Profile, SID, Program, ProgramID, Plugin, Config, Root, RootID string
}

// The caller holds the containing namespace and has checked its ID-D1
// descriptor. The read handle prevents writes and replacement during decode.
func readOpenCodeControlFile(path string, limit int64) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 || info.NumberOfLinks != 1 {
		return nil, errors.New("OpenCode authority record must be a regular single-name file")
	}
	if size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow); size > uint64(limit) {
		return nil, errors.New("OpenCode authority record exceeds its bound")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.Join(errors.New("OpenCode authority record exceeds its bound"), err)
	}
	return raw, nil
}

func openCodeAuthorityDir(state string) string { return filepath.Join(state, "apps", "opencode") }

// runtimeAppEndpoints selects the same exact names the existing constructors
// serve. Companion OA service admission changes no rights decision.
func runtimeAppEndpoints(options runtimeFlags) ([]string, error) {
	var endpoints []string
	add := func(endpoint string, err error) error {
		if err != nil {
			return err
		}
		for _, existing := range endpoints {
			if existing == endpoint {
				return nil
			}
		}
		endpoints = append(endpoints, endpoint)
		return nil
	}
	for _, item := range []struct {
		value, service string
		enabled        bool
	}{
		{options.endpoint, "runtime-v1", true}, {options.logEndpoint, "logging-v1", true},
		{options.configEndpoint, "config-v1", true}, {options.jobEndpoint, "job-acceptance-v1", !options.withoutJobs || options.jobRoot != ""},
		{options.modelEndpoint, "model-v1", !options.withoutModels},
	} {
		if !item.enabled {
			continue
		}
		if item.value == "" && item.service == "model-v1" && options.endpoint != "" {
			item.value = options.endpoint + "-model"
		}
		if item.value != "" {
			if err := add(item.value, nil); err != nil {
				return nil, err
			}
		} else if err := add(options.defaultEndpoint(item.service)); err != nil {
			return nil, err
		}
	}
	credentials, rights, _, err := credentialsEndpoints(options)
	if err != nil {
		return nil, err
	}
	for _, endpoint := range []string{credentials, rights} {
		if err := add(endpoint, nil); err != nil {
			return nil, err
		}
	}
	for _, derive := range []func(runtimeFlags) (string, error){inferenceEndpoint, inferenceRemoteEndpoint, asksEndpoint, runtimeRouterEndpoint, resourceTableEndpoint, registryEndpoint, lendingEndpoint} {
		if err := add(derive(options)); err != nil {
			return nil, err
		}
	}
	app := options.endpoint + "-applications"
	if options.endpoint == "" {
		app, err = options.defaultEndpoint("applications-v1")
	}
	if err := add(app, err); err != nil {
		return nil, err
	}
	content := options.endpoint + "-content"
	if options.endpoint == "" {
		content, err = options.defaultEndpoint("storage-content-v1")
	}
	if err := add(content, err); err != nil {
		return nil, err
	}
	return endpoints, nil
}

// configureRuntimeAppSandbox reserves connectivity for this runtime's one
// supported application profile before any listener constructor. Installation,
// code qualification and operation authority remain the launch and rights
// owners' decisions. Its release follows every listener, including partial
// startup cleanup.
func configureRuntimeAppSandbox(options runtimeFlags) (func() error, error) {
	profile, err := sandbox.ApplicationProfileName("opencode", "")
	if err != nil {
		return nil, err
	}
	sid, err := sandbox.ProfileSID(profile)
	if err != nil {
		return nil, err
	}
	endpoints, err := runtimeAppEndpoints(options)
	if err != nil {
		return nil, err
	}
	release, err := listen.ConfigureAdmission(endpoints, []string{sid})
	if err != nil {
		return nil, err
	}
	restore, err := identity.AdmitServerQueries(sid)
	if err != nil {
		release()
		return nil, err
	}
	return func() error { err := restore(); release(); return err }, nil
}
