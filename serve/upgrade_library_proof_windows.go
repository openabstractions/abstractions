package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// Keep this separate from exclusion.json for installed v1 activation readers.
// A durable intent precedes each rename. A commit marker lets later upgrades
// retire a still-mapped original after its client exits.
type upgradeLibraryProof struct {
	Version   int               `json:"version"`
	Scope     string            `json:"scope"`
	Record    string            `json:"record"`
	Installer processIdentity   `json:"installer"`
	Mover     processIdentity   `json:"mover"`
	Begun     time.Time         `json:"begun"`
	Tools     string            `json:"tools"`
	Aside     string            `json:"aside"`
	Libraries map[string]string `json:"libraries"`
	Committed bool              `json:"committed,omitempty"`
}

type upgradeLibraryLedger struct {
	env   exclusionEnv
	scope string
	path  string
}

func (l upgradeLibraryLedger) proofPath(record string) (string, error) {
	pid, stamp, ok := strings.Cut(record, "-")
	p, pidErr := strconv.ParseUint(pid, 10, 32)
	n, stampErr := strconv.ParseInt(stamp, 10, 64)
	if !ok || p == 0 || pidErr != nil || stampErr != nil || fmt.Sprintf("%d-%d", p, n) != record {
		return "", errors.New("invalid upgrade library record name")
	}
	return filepath.Join(filepath.Dir(l.path), "libraries-"+record+".json"), nil
}

func (l upgradeLibraryLedger) load(record string) (*upgradeLibraryProof, error) {
	path, err := l.proofPath(record)
	if err != nil {
		return nil, err
	}
	data, err := l.env.read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > exclusionMaxBytes {
		return nil, errors.New("upgrade library proof is too large")
	}
	if err := l.env.trusted(path, l.scope); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proof upgradeLibraryProof
	if err := decoder.Decode(&proof); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("upgrade library proof has trailing data")
	}
	if proof.Version != 1 || proof.Scope != l.scope || proof.Record != record || proof.Installer.PID == 0 ||
		proof.Installer.Created < 0 || proof.Mover.PID == 0 || proof.Mover.Created <= 0 || proof.Begun.IsZero() || fmt.Sprintf("%d-%d", proof.Installer.PID, proof.Begun.UnixNano()) != record || proof.Tools == "" || proof.Aside == "" || len(proof.Libraries) == 0 {
		return nil, errors.New("invalid upgrade library proof")
	}
	for name, key := range proof.Libraries {
		if !isUpgradeAsideLibrary(name) || key == "" {
			return nil, errors.New("invalid upgrade library entry in proof")
		}
	}
	return &proof, nil
}

func (l upgradeLibraryLedger) store(proof *upgradeLibraryProof) error {
	path, err := l.proofPath(proof.Record)
	if err != nil {
		return err
	}
	data, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	return l.env.writeUpgradeSidecar(path, l.scope, data)
}

func (l upgradeLibraryLedger) recordMove(record *upgradeExclusion, mover processIdentity, tools, aside, name, key string) error {
	nameOfRecord := upgradeAsideName(record)
	proof, err := l.load(nameOfRecord)
	if err != nil {
		return err
	}
	if proof == nil {
		proof = &upgradeLibraryProof{
			Version: 1, Scope: l.scope, Record: nameOfRecord, Installer: record.Installer, Mover: mover, Begun: record.Begun, Tools: tools, Aside: aside,
			Libraries: make(map[string]string),
		}
	}
	if proof.Installer != record.Installer || proof.Mover != mover || !proof.Begun.Equal(record.Begun) || proof.Tools != tools || proof.Aside != aside || proof.Committed {
		return errors.New("upgrade library directories changed after recording custody")
	}
	if existing := proof.Libraries[name]; existing != "" && existing != key {
		return errors.New("upgrade library changed after recording custody")
	}
	proof.Libraries[name] = key
	return l.store(proof)
}

func (l upgradeLibraryLedger) remove(record string) {
	path, err := l.proofPath(record)
	if err == nil {
		// Best effort after an empty transaction directory was cleaned up.
		_ = l.env.remove(path)
	}
}

func (p upgradeLibraryProof) matchDirectories(tools, aside *os.File) error {
	toolsKey, err := fileid.FileKeyOf(tools)
	if err != nil {
		return err
	}
	asideKey, err := fileid.FileKeyOf(aside)
	if err != nil {
		return err
	}
	if p.Tools != toolsKey || p.Aside != asideKey {
		return errors.New("upgrade library custody directories changed")
	}
	return nil
}

func (l upgradeLibraryLedger) ownsDirectories(toolsPath string, aside *os.File) bool {
	proof, err := l.load(filepath.Base(aside.Name()))
	if err != nil || proof == nil {
		return false
	}
	tools, release, err := holdUpgradeDirectory(toolsPath)
	if err != nil {
		return false
	}
	defer release()
	return proof.matchDirectories(tools, aside) == nil
}

// Upgrade activation may ignore an unverifiable holder after its deadline.
// Recovering its displaced DLLs requires affirmative evidence of process exit.
func upgradeInstallerEnded(holder processIdentity) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, holder.PID)
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(h)
	if holder.Created != 0 {
		if created, err := handleCreated(h); err == nil && created != holder.Created {
			return true
		}
	}
	state, err := windows.WaitForSingleObject(h, 0)
	return err == nil && state == windows.WAIT_OBJECT_0
}
