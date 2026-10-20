package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
)

func TestMacOSKeychainReopensBoundLocatorAfterDefaultChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	currentDefault := "/native/original.keychain-db"
	defaultCalls := 0
	var opened []string
	constructors := macOSKeychainConstructors{
		dataProtection: func() (credentials.Backend, error) { t.Fatal("selected replacement DP store"); return nil, nil },
		loginPath:      func() (string, error) { defaultCalls++; return currentDefault, nil },
		loginAt: func(path string) (credentials.Backend, error) {
			opened = append(opened, path)
			return keychainSelectionBackend(credentials.StoreMacOSLoginKeychain), nil
		},
		disableUI: func() error { return nil },
	}
	first := newMacOSCredentialsBackend(dir, constructors)
	if first.Name() != credentials.StoreMacOSLoginKeychain {
		t.Fatal(first.Name())
	}
	before, err := os.ReadFile(macOSKeychainMarker(dir))
	if err != nil {
		t.Fatal(err)
	}
	currentDefault = "/native/replacement.keychain-db"
	second := newMacOSCredentialsBackend(dir, constructors)
	if second.Name() != credentials.StoreMacOSLoginKeychain || defaultCalls != 1 || len(opened) != 2 ||
		opened[0] != "/native/original.keychain-db" || opened[1] != opened[0] {
		t.Fatalf("default change rebound native store: defaults=%d, opened=%v", defaultCalls, opened)
	}
	after, err := os.ReadFile(macOSKeychainMarker(dir))
	if err != nil || string(after) != string(before) {
		t.Fatal("restart changed native locator marker")
	}
}

func TestMacOSKeychainUnavailableBoundStorePreservesRegistration(t *testing.T) {
	for _, reason := range []string{"missing", "locked"} {
		t.Run(reason, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "credentials")
			selection, err := selectMacOSKeychainBackend(dir, func() (string, error) { return "/native/original.keychain-db", nil })
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(dir, "holder.json")
			holder, err := credentials.Open(credentials.Config{Backend: keychainSelectionBackend(credentials.StoreMacOSLoginKeychain), StatePath: state})
			if err != nil {
				t.Fatal(err)
			}
			owner := cwire.Subject{Account: "501", Program: filepath.Join(t.TempDir(), "receiver")}
			stored := holder.Store(owner, "", cwire.Registration{Name: "dummy", Kind: "bearer", Secret: []byte("dummy-only"),
				Scope: cwire.Scope{Targets: []string{"example.com"}, Consumers: []string{"abstraction.download/http-execution@1"}}})
			if stored.Outcome != cwire.StoreOutcomeStored {
				t.Fatal(stored.Outcome)
			}
			backend := newMacOSCredentialsBackend(dir, macOSKeychainConstructors{
				dataProtection: func() (credentials.Backend, error) {
					t.Fatal("unavailable native store fell back to DP")
					return nil, nil
				},
				loginPath: func() (string, error) { t.Fatal("unavailable native store scanned the new default"); return "", nil },
				loginAt: func(path string) (credentials.Backend, error) {
					if path != selection.KeychainPath {
						t.Fatal("wrong bound store reopened")
					}
					return nil, errors.New(reason + " native Keychain")
				},
				disableUI: func() error { return nil },
			})
			reopened, err := credentials.Open(credentials.Config{Backend: backend, StatePath: state})
			if err != nil {
				t.Fatal(err)
			}
			applied := reopened.Apply(context.Background(), cwire.Use{Subject: owner, Name: "dummy", Target: "example.com",
				Consumer: "abstraction.download/http-execution@1"}, func(context.Context, cwire.Subject, string, string) (string, error) { return "permitted", nil })
			if applied.Outcome != cwire.ApplyOutcomeUnavailable || applied.Headers != nil {
				t.Fatal("unavailable bound store returned loss or secret material")
			}
			page := reopened.List(owner.Account, "", 8)
			if page.Outcome != cwire.PageOutcomePage || len(page.Records) != 1 || page.Records[0].State != "active" || page.Records[0].Revision != stored.Revision {
				t.Fatal("unavailable native store changed registration")
			}
		})
	}
}

func TestMacOSKeychainUnboundLoginMarkerStaysUnavailable(t *testing.T) {
	for _, marker := range []string{credentials.StoreMacOSLoginKeychain + "\n",
		`{"backend":"macos-login-keychain"}`, `{"backend":"macos-login-keychain","keychain_path":"relative"}`,
		`{"backend":"macos-login-keychain","keychain_path":"/native/original","unknown":1}`} {
		dir := filepath.Join(t.TempDir(), "credentials")
		if err := os.WriteFile(macOSKeychainMarker(dir), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
		backend := newMacOSCredentialsBackend(dir, macOSKeychainConstructors{})
		if _, err := backend.Get("retained-item"); !errors.Is(err, credentials.ErrUnavailable) || errors.Is(err, credentials.ErrNotFound) {
			t.Fatalf("unbound login marker rebound or lost an item: %v", err)
		}
		retained, err := os.ReadFile(macOSKeychainMarker(dir))
		if err != nil || string(retained) != marker {
			t.Fatal("unbound marker silently rebound")
		}
	}
}

func TestMacOSKeychainFailedLocatorCaptureDoesNotBecomeLegacyDP(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	backend := newMacOSCredentialsBackend(dir, macOSKeychainConstructors{
		loginPath: func() (string, error) { return "", errors.New("native path unavailable") },
		disableUI: func() error { return nil },
	})
	if backend.Name() != credentials.StoreMacOSLoginKeychain {
		t.Fatal("fresh failure selected another kind")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	restarted := newMacOSCredentialsBackend(dir, macOSKeychainConstructors{})
	if restarted.Name() != credentials.StoreMacOSLoginKeychain {
		t.Fatal("failed fresh binding became legacy DP")
	}
	if _, err := restarted.Get("dummy"); !errors.Is(err, credentials.ErrUnavailable) {
		t.Fatal(err)
	}
}

type keychainSelectionBackend string

func (b keychainSelectionBackend) Name() string               { return string(b) }
func (b keychainSelectionBackend) MaxSecretBytes() int        { return credentials.MaxSecretBytes }
func (b keychainSelectionBackend) Put(string, []byte) error   { return nil }
func (b keychainSelectionBackend) Get(string) ([]byte, error) { return nil, credentials.ErrNotFound }
func (b keychainSelectionBackend) Delete(string) error        { return nil }

func TestMacOSKeychainSelectionRetainsStoreAcrossRestart(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "credentials")
		want := credentials.StoreMacOSLoginKeychain
		if legacy {
			want = credentials.StoreMacOSKeychain
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "holder.json"), []byte("retained legacy metadata"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		first, err := selectMacOSKeychainBackend(dir, func() (string, error) { return "/native/original.keychain-db", nil })
		if err != nil || first.Backend != want {
			t.Fatalf("first selection: %v, %v", first, err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		second, err := selectMacOSKeychainBackend(dir, func() (string, error) { t.Fatal("restart consulted the changed default"); return "", nil })
		if err != nil || second != first {
			t.Fatalf("restart selection: %v, %v", second, err)
		}
		marker, err := readMacOSKeychainMarker(macOSKeychainMarker(dir))
		if err != nil || marker != first {
			t.Fatalf("durable marker: %v, %v", marker, err)
		}
		if legacy {
			data, err := os.ReadFile(filepath.Join(dir, "holder.json"))
			if err != nil || string(data) != "retained legacy metadata" {
				t.Fatal("legacy metadata changed")
			}
		}
	}
}

func TestMacOSKeychainMarkerRefusesUnknownWithoutFallback(t *testing.T) {
	for _, word := range []string{"", "file-0600\n", "unknown-keychain\n", strings.Repeat("x", 8193)} {
		dir := filepath.Join(t.TempDir(), "credentials")
		path := macOSKeychainMarker(dir)
		if err := os.WriteFile(path, []byte(word), 0o600); err != nil {
			t.Fatal(err)
		}
		called := false
		constructor := func() (credentials.Backend, error) { called = true; return keychainSelectionBackend("unexpected"), nil }
		backend := newMacOSCredentialsBackend(dir, macOSKeychainConstructors{dataProtection: constructor,
			loginPath: func() (string, error) { called = true; return "/unexpected", nil },
			loginAt:   func(string) (credentials.Backend, error) { return constructor() }, disableUI: func() error { called = true; return nil }})
		if called || backend.Name() != unavailableMacOSKeychain {
			t.Fatalf("unknown marker selected a store: %t, %s", called, backend.Name())
		}
		if _, err := backend.Get("old-item"); !errors.Is(err, credentials.ErrUnavailable) || errors.Is(err, credentials.ErrNotFound) {
			t.Fatalf("unknown marker read: %v", err)
		}
		retained, err := os.ReadFile(path)
		if err != nil || string(retained) != word {
			t.Fatal("unknown marker overwritten")
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unknown marker created credential state")
		}
	}
}

func TestMacOSKeychainConstructorFailureKeepsSelectedStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	loginCalls, dpCalls, disabled := 0, 0, 0
	constructors := macOSKeychainConstructors{
		dataProtection: func() (credentials.Backend, error) {
			dpCalls++
			return keychainSelectionBackend(credentials.StoreMacOSKeychain), nil
		},
		loginPath: func() (string, error) { return "/native/original.keychain-db", nil },
		loginAt:   func(string) (credentials.Backend, error) { loginCalls++; return nil, credentials.ErrUnavailable },
		disableUI: func() error { disabled++; return nil },
	}
	backend := newMacOSCredentialsBackend(dir, constructors)
	if backend.Name() != credentials.StoreMacOSLoginKeychain || loginCalls != 1 || dpCalls != 0 || disabled != 1 {
		t.Fatal("failed login store substituted another backend")
	}
	if _, err := backend.Get("old-item"); !errors.Is(err, credentials.ErrUnavailable) {
		t.Fatalf("read: %v", err)
	}
	constructors.loginAt = func(string) (credentials.Backend, error) {
		return keychainSelectionBackend(credentials.StoreMacOSLoginKeychain), nil
	}
	if next := newMacOSCredentialsBackend(dir, constructors); next.Name() != credentials.StoreMacOSLoginKeychain || dpCalls != 0 {
		t.Fatal("restart changed backend")
	}
}

func TestMacOSKeychainLegacyDoesNotDisableInteraction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	backend := newMacOSCredentialsBackend(dir, macOSKeychainConstructors{
		dataProtection: func() (credentials.Backend, error) {
			return keychainSelectionBackend(credentials.StoreMacOSKeychain), nil
		},
		loginPath: func() (string, error) { t.Fatal("legacy selected login"); return "", nil },
		loginAt:   func(string) (credentials.Backend, error) { t.Fatal("legacy opened login"); return nil, nil },
		disableUI: func() error { t.Fatal("legacy changed process interaction"); return nil },
	})
	if backend.Name() != credentials.StoreMacOSKeychain {
		t.Fatal(backend.Name())
	}
}

func TestMacOSKeychainUnknownMarkerPreservesRegistration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	state := filepath.Join(dir, "holder.json")
	holder, err := credentials.Open(credentials.Config{Backend: keychainSelectionBackend(credentials.StoreMacOSKeychain), StatePath: state})
	if err != nil {
		t.Fatal(err)
	}
	owner := cwire.Subject{Account: "501", Program: filepath.Join(t.TempDir(), "receiver")}
	stored := holder.Store(owner, "", cwire.Registration{Name: "dummy", Kind: "bearer", Secret: []byte("dummy-only"),
		Scope: cwire.Scope{Targets: []string{"example.com"}, Consumers: []string{"abstraction.download/http-execution@1"}}})
	if stored.Outcome != cwire.StoreOutcomeStored {
		t.Fatal(stored.Outcome)
	}
	if err := os.WriteFile(macOSKeychainMarker(dir), []byte("unknown-store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	constructor := func() (credentials.Backend, error) { t.Fatal("unknown marker opened a store"); return nil, nil }
	backend := newMacOSCredentialsBackend(dir, macOSKeychainConstructors{dataProtection: constructor,
		loginPath: func() (string, error) { t.Fatal("unknown marker consulted default"); return "", nil },
		loginAt:   func(string) (credentials.Backend, error) { return constructor() },
		disableUI: func() error { t.Fatal("unknown marker changed process UI"); return nil }})
	reopened, err := credentials.Open(credentials.Config{Backend: backend, StatePath: state})
	if err != nil {
		t.Fatal(err)
	}
	page := reopened.List(owner.Account, "", 8)
	if page.Outcome != cwire.PageOutcomePage || len(page.Records) != 1 || page.Records[0].State != "active" || page.Records[0].Revision != stored.Revision {
		t.Fatal("unavailable marker changed existing registration")
	}
}
