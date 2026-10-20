package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestServiceUpgradeInstalledChainArguments(t *testing.T) {
	token := strings.Repeat("a", 32)
	r, err := serviceArguments([]string{"begin-upgrade", "--machine", "--installed", "--chain", token})
	if err != nil || !r.installed || r.scope != "machine" || r.chain != token {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = serviceArguments([]string{"begin-upgrade", "--machine", machineFolder, "--related", coreUpgradeCode, "--chain", ""})
	if err != nil || r.chain != "" || r.installed {
		t.Fatalf("bare MSI %+v %v", r, err)
	}
	for _, args := range [][]string{
		{"begin-upgrade", "--user", "--installed", "--chain", token},
		{"begin-upgrade", "--machine", "--installed", "--chain", ""},
		{"begin-upgrade", "--machine", "--installed", "--chain", "untrusted"},
	} {
		if _, err := serviceArguments(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestServiceRemoveLoginArgumentsAndDiagnosticScope(t *testing.T) {
	r, err := serviceArguments([]string{"remove-login", "--user", `C:\prior tools\.`})
	if err != nil || r.command != "remove-login" || r.folder != `C:\prior tools\.` || r.scope != "user" {
		t.Fatalf("%+v %v", r, err)
	}
	if command, scope := installerAction(r); command != "remove-login --user" || scope != "user" {
		t.Fatalf("%s %s", command, scope)
	}
	for _, args := range [][]string{{"remove-login"}, {"remove-login", "--machine", `C:\tools`}, {"remove-login", "--user", ""}, {"remove-login", "--user", `C:\tools`, "extra"}} {
		if _, err := serviceArguments(args); err == nil {
			t.Fatal(args)
		}
	}
}

func TestServiceRemoveLoginAfterPayloadRemovalPreservesForeign(t *testing.T) {
	// No files or installation exist at this path. Commit cleanup uses only the
	// prior fixed commands and injected registry values, never a live selector.
	folder := `C:\removed installation\tools`
	runtime, _ := runtimeLogonCommand(filepath.Join(folder, windowlessName), "serve supervisor")
	values := map[string]logonValue{runtimeLogonName: runtime, presenterLogonName: {registry.SZ, []byte("foreign")}}
	removed := 0
	err := removeWindowsLogon(context.Background(), folder+`\.`, func(name string) (logonValue, bool, error) { v, ok := values[name]; return v, ok, nil }, func(name string) error { removed++; delete(values, name); return nil })
	if err == nil || removed != 1 || len(values) != 1 {
		t.Fatalf("err=%v removed=%d values=%v", err, removed, values)
	}
}

func TestServiceRemoveLoginRefusesInvalidFolderAndChangedValue(t *testing.T) {
	for _, folder := range []string{"", `relative`, `C:tools`, `\\server\share\tools`, `C:\tools\..\other`, `C:/tools`} {
		err := removeWindowsLogon(context.Background(), folder, func(string) (logonValue, bool, error) {
			t.Fatal("invalid folder read registry")
			return logonValue{}, false, nil
		}, func(string) error { t.Fatal("invalid folder deleted registry"); return nil })
		if err == nil {
			t.Fatal(folder)
		}
	}
	folder := `C:\prior\tools`
	value, _ := runtimeLogonCommand(filepath.Join(folder, windowlessName), "serve supervisor")
	reads := 0
	err := removeWindowsLogon(context.Background(), folder, func(name string) (logonValue, bool, error) {
		if name != runtimeLogonName {
			return logonValue{}, false, nil
		}
		reads++
		if reads > 1 {
			return logonValue{registry.SZ, []byte("changed")}, true, nil
		}
		return value, true, nil
	}, func(string) error { t.Fatal("changed value deleted"); return nil })
	if err == nil {
		t.Fatal("changed input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeWindowsLogon(ctx, folder, func(string) (logonValue, bool, error) { t.Fatal("canceled read"); return logonValue{}, false, nil }, func(string) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
