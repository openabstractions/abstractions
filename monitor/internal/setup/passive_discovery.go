package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openabstractions/abstraction-identity/fileid"
)

const discoveryLimit = 1 << 20

type discoveryDescriptor struct {
	Root          string             `json:"root"`
	Record        string             `json:"record"`
	Entries       string             `json:"entries"`
	InstallPath   string             `json:"install_path"`
	Status        discoveryStatusMap `json:"status"`
	Cloud         discoveryCloudMap  `json:"cloud"`
	RemoteAddress string             `json:"remote_address,omitempty"`
	Singleton     json.RawMessage    `json:"singleton,omitempty"`
}

type discoverySingleton struct {
	Root         string   `json:"root"`
	Record       string   `json:"record"`
	InstallPaths []string `json:"install_paths"`
}

type discoveryStatusMap struct {
	Pointer   string `json:"pointer"`
	Installed string `json:"installed"`
}

type discoveryCloudMap struct {
	Pointer string  `json:"pointer"`
	Equals  *string `json:"equals,omitempty"`
}

type discoveryRoots struct {
	local, roaming, applicationSupport string
}

type discoveryRecord struct {
	path             string
	installed, cloud bool
}

func parseDiscoveryDescriptor(raw json.RawMessage) (discoveryDescriptor, error) {
	var d discoveryDescriptor
	if err := decodeDiscoveryDeclaration(raw, &d); err != nil {
		return d, err
	}
	return d, nil
}

func validateDiscoveryArray(d discoveryDescriptor) error {
	if err := validateDiscoveryLocation(d.Root, d.Record); err != nil {
		return err
	}
	if d.Status.Installed == "" || len(d.Status.Installed) > 64 ||
		(d.Cloud.Equals != nil && (*d.Cloud.Equals == "" || len(*d.Cloud.Equals) > 64)) {
		return errors.New("invalid discovery comparison value")
	}
	if !validDiscoveryPointer(d.Entries, true) || !validDiscoveryPointer(d.InstallPath, false) ||
		!validDiscoveryPointer(d.Status.Pointer, false) || !validDiscoveryPointer(d.Cloud.Pointer, false) ||
		!validDiscoveryPointer(d.RemoteAddress, true) {
		return errors.New("invalid discovery JSON pointer")
	}
	return nil
}

func decodeDiscoveryDeclaration(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing content in discovery declaration")
	}
	return nil
}

func validateDiscoveryLocation(root, record string) error {
	switch root {
	case "LocalAppData", "RoamingAppData", "Application Support":
	default:
		return errors.New("unknown discovery root")
	}
	if len(record) > 260 || strings.ContainsAny(record, `\:`) {
		return errors.New("invalid discovery record path")
	}
	for _, part := range strings.Split(record, "/") {
		if fileid.SafeName(part) != nil {
			return errors.New("invalid discovery record path")
		}
	}
	return nil
}

func validDiscoveryPointer(pointer string, allowRoot bool) bool {
	if pointer == "" {
		return allowRoot
	}
	if !strings.HasPrefix(pointer, "/") || len(pointer) > 256 {
		return false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			i++
			if i == len(pointer) || (pointer[i] != '0' && pointer[i] != '1') {
				return false
			}
		}
	}
	return true
}

// discoveryPointer traverses object keys only. Only the entries pointer can
// select an array, and its caller iterates that array once.
func discoveryPointer(raw json.RawMessage, pointer string) (json.RawMessage, error) {
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if pointer == "" {
			break
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		key := strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		raw = object[key]
		if raw == nil {
			return nil, nil
		}
	}
	return raw, nil
}

func discoveryValue(raw json.RawMessage, pointer string, value any) error {
	field, err := discoveryPointer(raw, pointer)
	if err != nil || field == nil {
		return err
	}
	return json.Unmarshal(field, value)
}

func (d discoveryDescriptor) decode(raw json.RawMessage) (discoveryRecord, error) {
	var r discoveryRecord
	var status string
	if err := discoveryValue(raw, d.InstallPath, &r.path); err != nil {
		return r, err
	}
	if err := discoveryValue(raw, d.Status.Pointer, &status); err != nil {
		return r, err
	}
	r.installed = status == d.Status.Installed
	if d.Cloud.Equals == nil {
		if err := discoveryValue(raw, d.Cloud.Pointer, &r.cloud); err != nil {
			return r, err
		}
	} else {
		var cloud string
		if err := discoveryValue(raw, d.Cloud.Pointer, &cloud); err != nil {
			return r, err
		}
		r.cloud = cloud == *d.Cloud.Equals
	}
	if d.RemoteAddress != "" {
		field, err := discoveryPointer(raw, d.RemoteAddress)
		if err != nil {
			return r, err
		}
		if field != nil {
			var address *string
			if err := json.Unmarshal(field, &address); err != nil || address == nil {
				return r, errors.New("invalid discovery remote address")
			}
			r.cloud = r.cloud || *address != ""
		}
	}
	return r, nil
}

func readDiscovery(ctx context.Context, d discoveryDescriptor, roots discoveryRoots, visit func(int, discoveryRecord, error)) error {
	if err := validateDiscoveryArray(d); err != nil {
		return err
	}
	root := roots.lookup(d.Root)
	if root == "" {
		return nil
	}
	raw, err := readDiscoveryFile(ctx, root, d.Record)
	if err != nil {
		return err
	}
	array, err := discoveryPointer(raw, d.Entries)
	if err != nil {
		return err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(array, &entries); err != nil {
		return err
	}
	for i, entry := range entries {
		r, err := d.decode(entry)
		visit(i, r, err)
	}
	return nil
}

func (r discoveryRoots) lookup(name string) string {
	var root string
	switch name {
	case "LocalAppData":
		root = r.local
	case "RoamingAppData":
		root = r.roaming
	case "Application Support":
		root = r.applicationSupport
	}
	return root
}

// readDiscoverySingleton offers hints in declaration order. Setup decides
// whether the first usable path names a folder it can show.
func readDiscoverySingleton(ctx context.Context, descriptor discoveryDescriptor, roots discoveryRoots,
	accept func(string) bool) error {
	if len(descriptor.Singleton) == 0 {
		return nil
	}
	var d discoverySingleton
	if err := decodeDiscoveryDeclaration(descriptor.Singleton, &d); err != nil {
		return err
	}
	if err := validateDiscoveryLocation(d.Root, d.Record); err != nil {
		return err
	}
	if len(d.InstallPaths) == 0 || len(d.InstallPaths) > 4 {
		return errors.New("invalid discovery singleton hint count")
	}
	for _, pointer := range d.InstallPaths {
		if !validDiscoveryPointer(pointer, false) {
			return errors.New("invalid discovery singleton hint pointer")
		}
	}
	root := roots.lookup(d.Root)
	if root == "" {
		return nil
	}
	raw, err := readDiscoveryFile(ctx, root, d.Record)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("invalid discovery singleton object")
	}
	for i, pointer := range d.InstallPaths {
		field, err := discoveryPointer(raw, pointer)
		var path *string
		if err != nil || (field != nil && (json.Unmarshal(field, &path) != nil || path == nil)) {
			logDiscoveryOnce("setup: desktop settings hint %d has another shape", i)
			continue
		}
		if path != nil && *path != "" && accept(*path) {
			break
		}
	}
	return nil
}

var discoveryDiagnostics sync.Map

// The Panel polls discovery. Write each diagnostic once per process.
func logDiscoveryOnce(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if _, seen := discoveryDiagnostics.LoadOrStore(line, true); !seen {
		log.Print(line)
	}
}

func readDiscoveryFile(ctx context.Context, root, relative string) ([]byte, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("discovery root is not a clean absolute path")
	}
	volume := filepath.VolumeName(root)
	volumeRoot := volume + string(filepath.Separator)
	parts := strings.FieldsFunc(strings.TrimPrefix(root, volumeRoot), func(r rune) bool { return r == rune(filepath.Separator) })
	parts = append(parts, strings.Split(relative, "/")...)
	h, err := fileid.Open(ctx, fileid.Self, volumeRoot, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	defer func() { h.Close() }()
	for i, part := range parts {
		if err := checkDiscoveryObject(h.File(), true); err != nil {
			return nil, err
		}
		next, err := h.Beneath(part, fileid.Options{})
		if err != nil {
			return nil, err
		}
		if i == len(parts)-1 {
			defer next.Close()
			if err := checkDiscoveryObject(next.File(), false); err != nil {
				return nil, err
			}
			read, err := h.Beneath(part, fileid.Options{Read: true})
			if err != nil {
				return nil, err
			}
			defer read.Close()
			if read.FileID().Key() != next.FileID().Key() {
				return nil, errors.New("discovery file changed while opening")
			}
			if err := checkDiscoveryObject(read.File(), false); err != nil {
				return nil, err
			}
			return readDiscoveryBytes(read.File())
		}
		h.Close()
		h = next
	}
	return nil, errors.New("discovery record path is empty")
}

func checkDiscoveryObject(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if discoveryReparse(info) || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return errors.New("discovery path is linked or has the wrong type")
	}
	return nil
}

func readDiscoveryBytes(file io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(file, discoveryLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > discoveryLimit {
		return nil, fmt.Errorf("discovery file exceeds %d bytes", discoveryLimit)
	}
	return raw, nil
}
