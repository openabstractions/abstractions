package main

import (
	"os"
	"path/filepath"
	"testing"

	runtimehost "github.com/openabstractions/abstraction-facade/go/runtime"
	logging "github.com/openabstractions/abstraction-logging/go"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

func TestPrivateRuntimeDoesNotObserveDefaultProfile(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "default-profile")
	for _, key := range []string{"APPDATA", "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "ProgramData"} {
		t.Setenv(key, profile)
	}
	private := filepath.Join(root, "selected-runtime")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	log, err := logging.OpenFileSink(filepath.Join(private, "records"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	endpoint := fixture.Endpoint(root, "rust-private")
	options := privateRuntimeOptions(endpoint, private, log)
	h, err := runtimehost.Listen(options)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := os.Lstat(profile); !os.IsNotExist(err) {
		t.Fatalf("private fixture reached default profile: %v", err)
	}
	if options.ConfigStore == nil || !options.ConfigWithoutMachine || options.ConfigUserKey != filepath.Join(private, "config", "user.json") || options.ConfigObservationSource == nil {
		t.Fatal("private fixture requires explicit bounded storage, native file observation and no machine rung")
	}
	if _, err := os.Stat(filepath.Dir(options.ConfigUserKey)); err != nil {
		t.Fatalf("actual Listen did not prepare its selected private observation: %v", err)
	}
}
