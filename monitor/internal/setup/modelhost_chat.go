package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
	localsource "github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// ModelHostChatRoot names one operator-chosen directory as a separate,
// Setup-owned inventory store. Its identifiers derive from the selected
// directory's file identity, never from path spelling or a machine default.
type ModelHostChatRoot struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Store        string `json:"store"`
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint"`
	ConfigPath   string `json:"config_path"`
	ReceiptPath  string `json:"receipt_path"`
	ConfigSHA256 string `json:"config_sha256"`
}

// NewModelHostChatRoot validates the user's local directory and derives stable
// Setup-owned names beneath configRoot. It reads no file inside the chosen
// directory.
func NewModelHostChatRoot(path, configRoot string) (ModelHostChatRoot, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return ModelHostChatRoot{}, errors.New("choose an absolute folder path on this computer")
	}
	if err := identity.LocalPath(path); err != nil {
		return ModelHostChatRoot{}, errors.New("choose a folder on a local drive, not a network or device path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return ModelHostChatRoot{}, errors.New("that folder cannot be opened")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || !filepath.IsAbs(resolved) {
		return ModelHostChatRoot{}, errors.New("that folder does not resolve to an absolute local path")
	}
	if err := identity.LocalPath(resolved); err != nil {
		return ModelHostChatRoot{}, errors.New("that folder resolves to a network or device path")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return ModelHostChatRoot{}, errors.New("choose an existing folder")
	}
	if configRoot == "" || !filepath.IsAbs(configRoot) {
		return ModelHostChatRoot{}, errors.New("Setup has no absolute per-user configuration folder")
	}
	cleanConfig, err := filepath.Abs(filepath.Clean(configRoot))
	if err != nil {
		return ModelHostChatRoot{}, errors.New("Setup has no absolute per-user configuration folder")
	}
	key, err := identity.FileKey(resolved)
	if err != nil {
		return ModelHostChatRoot{}, errors.New("that folder's local identity cannot be read")
	}
	sum := sha256.Sum256([]byte("directory-file:" + key))
	id := hex.EncodeToString(sum[:12])
	root := filepath.Join(cleanConfig, "modelhost-chat", id)
	return ModelHostChatRoot{
		ID:          id,
		Path:        resolved,
		Store:       "oa-chat-" + id,
		Provider:    "oa-setup-chat-" + id,
		Endpoint:    "oa_setup_chat_" + id,
		ConfigPath:  filepath.Join(root, "inventoryd.json"),
		ReceiptPath: filepath.Join(root, "receipt.json"),
	}, nil
}

// ModelHostChatInventoryConfig is the strict inventoryd operator config for
// this one approved root. It writes no general environment roots.
func ModelHostChatInventoryConfig(root ModelHostChatRoot) ([]byte, error) {
	if !modelHostID(root.ID) || root.Store != "oa-chat-"+root.ID || root.Path == "" || !filepath.IsAbs(root.Path) {
		return nil, errors.New("invalid Setup model store")
	}
	setupRoot, err := localsource.NewSetupInventoryRoot(root.Path, root.Store)
	if err != nil {
		if strings.Contains(err.Error(), "identity-derived store") {
			return nil, errors.New("the selected folder identity changed; select it again")
		}
		return nil, errors.New("the selected folder is no longer available; select it again")
	}
	if setupRoot.Path != root.Path {
		return nil, errors.New("the selected folder identity changed; select it again")
	}
	return localsource.SetupInventoryRootConfig(setupRoot)
}

// ModelHostChatApproval is the exact, human-readable effect the chooser must
// show before it writes an inventory config or registers a provider.
func ModelHostChatApproval(root ModelHostChatRoot) string {
	return fmt.Sprintf("Allow OpenAbstractions to read folder listings, file names, sizes and dates, GGUF headers, and ZIP directory entries in the selected folder %s. The folder identity is checked each time inventoryd reads it; replacing the folder stops inventory until you select it again. It will not change model files. Setup will write %s and register inventoryd as %s for the separate store %s, with permission to report this store to the runtime. No model is copied or started.",
		root.Path, root.ConfigPath, root.Provider, root.Store)
}

// ModelHostChatApprovalDigest binds the confirmation to its exact sentence.
func ModelHostChatApprovalDigest(sentence string) string {
	sum := sha256.Sum256([]byte(sentence))
	return hex.EncodeToString(sum[:])
}

func modelHostID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
