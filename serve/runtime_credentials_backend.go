package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	credentials "github.com/openabstractions/abstraction-credentials/go"
)

const unavailableMacOSKeychain = "macos-keychain-unavailable"

type macOSKeychainSelection struct {
	Backend      string `json:"backend"`
	KeychainPath string `json:"keychain_path,omitempty"`
}

func validMacOSKeychainPath(path string) bool {
	return strings.HasPrefix(path, "/") && len(path) <= 4095 && !strings.ContainsRune(path, 0) && utf8.ValidString(path)
}

// The marker is beside the credentials directory so a fresh choice is durable
// before that directory is created. Concurrent starts cannot mistake the first
// start's new empty directory for legacy unmarked data-protection state.
func macOSKeychainMarker(dir string) string {
	return filepath.Join(filepath.Dir(dir), "credentials-keychain-backend")
}

func readMacOSKeychainMarker(path string) (macOSKeychainSelection, error) {
	var selection macOSKeychainSelection
	info, err := os.Lstat(path)
	if err != nil {
		return selection, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return selection, errors.New("credentials: invalid macOS Keychain backend marker")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return selection, err
	}
	name := strings.TrimSpace(string(data))
	if name == credentials.StoreMacOSKeychain {
		return macOSKeychainSelection{Backend: name}, nil
	}
	if name == credentials.StoreMacOSLoginKeychain {
		return macOSKeychainSelection{Backend: name}, errors.New("credentials: login-Keychain marker lacks its native store locator")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&selection); err != nil {
		return macOSKeychainSelection{}, errors.New("credentials: unrecognized macOS Keychain backend marker")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return macOSKeychainSelection{}, errors.New("credentials: invalid macOS Keychain backend marker")
	}
	if selection.Backend == credentials.StoreMacOSLoginKeychain && validMacOSKeychainPath(selection.KeychainPath) {
		return selection, nil
	}
	if selection.Backend == credentials.StoreMacOSKeychain && selection.KeychainPath == "" {
		return selection, nil
	}
	return selection, errors.New("credentials: invalid macOS Keychain backend marker")
}

func selectMacOSKeychainBackend(dir string, loginPath func() (string, error)) (macOSKeychainSelection, error) {
	marker := macOSKeychainMarker(dir)
	if selection, err := readMacOSKeychainMarker(marker); !errors.Is(err, fs.ErrNotExist) {
		return selection, err
	}
	selection := macOSKeychainSelection{Backend: credentials.StoreMacOSKeychain}
	var pathErr error
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		selection.Backend = credentials.StoreMacOSLoginKeychain
		selection.KeychainPath, pathErr = loginPath()
		if pathErr == nil && !validMacOSKeychainPath(selection.KeychainPath) {
			pathErr = errors.New("credentials: native user Keychain locator is invalid")
		}
		if pathErr != nil {
			// Preserve the deliberate kind when native locator capture fails.
			// This unbound marker remains unavailable until explicitly repaired.
			selection.KeychainPath = ""
		}
	} else if err != nil {
		return selection, err
	}
	data := selection.Backend + "\n"
	if selection.KeychainPath != "" {
		encoded, encodeErr := json.Marshal(selection)
		if encodeErr != nil {
			return selection, encodeErr
		}
		data = string(encoded) + "\n"
	}
	if err := writeMarkerOnce(marker, data); errors.Is(err, fs.ErrExist) {
		// A partly written marker refuses rather than selecting another store.
		// Its complete durable word is used on the next start.
		return readMacOSKeychainMarker(marker)
	} else if err != nil {
		return selection, err
	}
	return selection, pathErr
}

// writeMarkerOnce creates a store marker exclusively and makes it durable. It
// returns an error wrapping fs.ErrExist when another start wrote it first.
func writeMarkerOnce(marker, data string) error {
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.WriteString(data); err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	// On macOS, persist the directory entry as well as the marker's contents.
	// Cross-platform selector tests exercise the same exclusive-file behavior.
	if runtime.GOOS == "darwin" {
		parent, err := os.Open(filepath.Dir(marker))
		if err != nil {
			return err
		}
		err = parent.Sync()
		closed = parent.Close()
		if err != nil {
			return err
		}
		if closed != nil {
			return closed
		}
	}
	return nil
}

// unavailableWindowsStore is the store word of a Windows state whose marker
// names no store this runtime knows.
const unavailableWindowsStore = "windows-store-unavailable"

// windowsCredentialsMarker is beside the credentials directory, as the macOS
// marker is, so a fresh choice is durable before that directory exists.
func windowsCredentialsMarker(dir string) string {
	return filepath.Join(filepath.Dir(dir), "credentials-windows-backend")
}

func readWindowsCredentialsMarker(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 256 {
		return "", errors.New("credentials: invalid Windows credentials store marker")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	switch name := strings.TrimSpace(string(data)); name {
	case credentials.StoreWindowsDPAPIFile, credentials.StoreWindowsCredentialManager:
		return name, nil
	}
	return "", errors.New("credentials: unrecognized Windows credentials store marker")
}

// selectWindowsCredentialsStore reads the state's durable store marker, or
// writes it once. A fresh state, with no credentials directory yet, binds to
// windows-dpapi-file. An unmarked existing state keeps
// windows-credential-manager, where its registrations' items are, until an
// explicit verified migration (abstraction-credentials CONTRACT.md CRED-B1).
func selectWindowsCredentialsStore(dir string) (string, error) {
	marker := windowsCredentialsMarker(dir)
	if name, err := readWindowsCredentialsMarker(marker); !errors.Is(err, fs.ErrNotExist) {
		return name, err
	}
	name := credentials.StoreWindowsCredentialManager
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		name = credentials.StoreWindowsDPAPIFile
	} else if err != nil {
		return "", err
	}
	if err := writeMarkerOnce(marker, name+"\n"); errors.Is(err, fs.ErrExist) {
		return readWindowsCredentialsMarker(marker)
	} else if err != nil {
		return "", err
	}
	return name, nil
}

type windowsCredentialsConstructors struct {
	credentialManager func() (credentials.Backend, error)
	dpapiFile         func(dir string) (credentials.Backend, error)
}

// newWindowsCredentialsBackend opens the store the state is bound to. A store
// that cannot be selected or opened stays configured and reads unavailable;
// nothing substitutes another store.
func newWindowsCredentialsBackend(dir string, constructors windowsCredentialsConstructors) credentials.Backend {
	name, err := selectWindowsCredentialsStore(dir)
	if err != nil {
		if name == "" {
			name = unavailableWindowsStore
		}
		return credentials.Unreachable(name, credentials.MaxSecretBytes, err)
	}
	var backend credentials.Backend
	if name == credentials.StoreWindowsDPAPIFile {
		backend, err = constructors.dpapiFile(filepath.Join(dir, "items"))
	} else {
		backend, err = constructors.credentialManager()
	}
	if err != nil {
		return credentials.Unreachable(name, credentials.MaxSecretBytes, err)
	}
	return backend
}

type macOSKeychainConstructors struct {
	dataProtection func() (credentials.Backend, error)
	loginPath      func() (string, error)
	loginAt        func(string) (credentials.Backend, error)
	disableUI      func() error
}

func newMacOSCredentialsBackend(dir string, constructors macOSKeychainConstructors) credentials.Backend {
	interactionDisabled := false
	selection, err := selectMacOSKeychainBackend(dir, func() (string, error) {
		if err := constructors.disableUI(); err != nil {
			return "", err
		}
		interactionDisabled = true
		return constructors.loginPath()
	})
	name := selection.Backend
	if name != credentials.StoreMacOSKeychain && name != credentials.StoreMacOSLoginKeychain {
		name = unavailableMacOSKeychain
	}
	if err != nil {
		return credentials.Unreachable(name, credentials.MaxSecretBytes, err)
	}
	var backend credentials.Backend
	if name == credentials.StoreMacOSLoginKeychain {
		if !interactionDisabled {
			err = constructors.disableUI()
		}
		if err == nil {
			backend, err = constructors.loginAt(selection.KeychainPath)
		}
	} else {
		backend, err = constructors.dataProtection()
	}
	if err != nil {
		return credentials.Unreachable(name, credentials.MaxSecretBytes, err)
	}
	if backend == nil {
		return credentials.Unreachable(name, credentials.MaxSecretBytes, errors.New("credentials: configured Keychain is unavailable"))
	}
	return backend
}

var loginKeychainReceiverInitialization struct {
	sync.Once
	err error
}

func initializeLoginKeychainReceiver() error {
	loginKeychainReceiverInitialization.Do(func() {
		loginKeychainReceiverInitialization.err = credentials.DisableLoginKeychainInteraction()
	})
	return loginKeychainReceiverInitialization.err
}

// Called before creating the credentials directory or opening Holder. Legacy
// unmarked state stays on its store; a fresh state binds to the user Keychain
// on macOS and to the DPAPI file store on Windows.
func newPlatformCredentialsBackend(dir string) (credentials.Backend, error) {
	if runtime.GOOS == "windows" {
		return newWindowsCredentialsBackend(dir, windowsCredentialsConstructors{
			credentialManager: credentials.NewCredentialManager,
			dpapiFile:         credentials.NewDPAPIFileBackend,
		}), nil
	}
	if runtime.GOOS != "darwin" {
		return platformBackend(dir)
	}
	return newMacOSCredentialsBackend(dir, macOSKeychainConstructors{
		dataProtection: credentials.NewKeychain,
		loginPath:      credentials.DefaultLoginKeychainPath,
		loginAt:        credentials.NewLoginKeychainAt,
		disableUI:      initializeLoginKeychainReceiver,
	}), nil
}
