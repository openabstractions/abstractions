//go:build windows

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	"golang.org/x/sys/windows"
)

func TestRuntimeAppAdmissionUsesConfiguredListenerNames(t *testing.T) {
	options := runtimeFlags{endpoint: `\\.\pipe\oa-app-fixture`, logEndpoint: `\\.\pipe\oa-app-log`, configEndpoint: `\\.\pipe\oa-app-config`,
		jobEndpoint: `\\.\pipe\oa-app-jobs`, stateDir: t.TempDir()}
	endpoints, err := runtimeAppEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{options.endpoint, options.logEndpoint, options.configEndpoint, options.jobEndpoint,
		options.endpoint + "-model", options.endpoint + "-credentials", options.endpoint + "-rights", options.endpoint + "-inference",
		options.endpoint + "-inference-remote", options.endpoint + "-asks", options.endpoint + "-router", options.endpoint + "-resource-table",
		options.endpoint + "-registry", options.endpoint + "-lend", options.endpoint + "-applications", options.endpoint + "-content"} {
		if !slices.Contains(endpoints, endpoint) {
			t.Fatalf("missing actual configured listener %s from %v", endpoint, endpoints)
		}
	}
	if len(endpoints) != 16 {
		t.Fatalf("unexpected extra admitted endpoint: %v", endpoints)
	}
	options.withoutJobs, options.withoutModels = true, true
	endpoints, err = runtimeAppEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(endpoints, options.jobEndpoint) || slices.Contains(endpoints, options.endpoint+"-model") {
		t.Fatalf("disabled services admitted: %v", endpoints)
	}
}

func TestRuntimeAppAdmissionBeforeSetupReservesOnlyItsSupportedProfile(t *testing.T) {
	state := t.TempDir()
	endpoint := `\\.\pipe\oa-before-setup-` + filepath.Base(state)
	release, err := configureRuntimeAppSandbox(runtimeFlags{endpoint: endpoint, stateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
		t.Fatalf("absent record created state: %v %v", entries, err)
	}
	profile, err := sandbox.ApplicationProfileName("opencode", "")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := sandbox.ProfileSID(profile)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect ordinary positive fixture descriptors, without launching a guest.
	// Unconfigured listeners keep the shared owner's account-only default.
	for _, fixture := range []struct {
		name string
		want []string
	}{
		{endpoint, []string{sid}}, {endpoint + "-unconfigured", nil},
	} {
		listener, err := listen.Listen(fixture.name)
		if err != nil {
			t.Fatal(err)
		}
		name, err := windows.UTF16PtrFromString(fixture.name)
		if err != nil {
			listener.Close()
			t.Fatal(err)
		}
		handle, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			listener.Close()
			t.Fatal(err)
		}
		descriptor, readErr := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
		closeErr := windows.CloseHandle(handle)
		listenerErr := listener.Close()
		if readErr != nil || closeErr != nil || listenerErr != nil {
			t.Fatalf("fixture descriptor read/close: %v %v %v", readErr, closeErr, listenerErr)
		}
		got := regexp.MustCompile(`S-1-15-2(?:-[0-9]+){7}`).FindAllString(descriptor.String(), -1)
		if !slices.Equal(got, fixture.want) {
			t.Fatalf("profile admission for %s = %v, want %v", fixture.name, got, fixture.want)
		}
	}
}

func TestRuntimeAppAdmissionPreservesEndpointAndUnconfiguredStateDefaults(t *testing.T) {
	state := t.TempDir()
	options := runtimeFlags{withoutJobs: true, withoutModels: true}
	endpoints, err := runtimeAppEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"runtime-v1", "logging-v1", "config-v1", "asks-application-v1", "inference-v1", "storage-content-v1"} {
		endpoint, err := bootstrap.Endpoint(service)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(endpoints, endpoint) {
			t.Fatalf("default %s changed: %v", service, endpoints)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "apps")); !os.IsNotExist(err) {
		t.Fatalf("unconfigured state touched: %v", err)
	}
}
