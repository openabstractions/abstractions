package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// File-backed stores. A declared provider that names a store resource
// (store:<name>) without the inventory-source contract publishes what Setup
// installed for it as installed.json in its private state folder,
// <account data>/OpenAbstractions/providers/<name>. The runtime reads that
// record on the inventory clock, re-hashes each listed file whose size or
// write time changed, and tells the router the files whose bytes match as
// held with hashed evidence. It never starts the provider to read it. A
// declaration's recipes that name those files by digest are installed only
// while every file is held (router CONTRACT.md, ROUTE recipe rule).
const (
	installedRecordName  = "installed.json"
	installedRecordLimit = 1 << 20
	installedMaxFiles    = 256
)

// installedStoresRoot is the folder each file-backed store's state folder
// sits in. Tests replace it so no test reads the person's own providers.
var installedStoresRoot = defaultInstalledStoresRoot

// defaultInstalledStoresRoot is OpenAbstractions/providers under
// layout.AccountData (ST-2 ruling 2a).
func defaultInstalledStoresRoot() (string, error) { return layout.DefaultProviderStateRoot() }

var installedDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// installedRecord is installed.json, version 1.
type installedRecord struct {
	Version int `json:"version"`
	Models  []struct {
		Model string `json:"model"`
		Files []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	} `json:"models"`
}

// installedHash is one file's digest as last hashed, valid while its size
// and write time stay the same.
type installedHash struct {
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Digest   string    `json:"sha256"`
}

// installedStores reads the file-backed stores on its own goroutine, so the
// inventory refresh never waits on hashing: each refresh takes the last
// finished reading and starts the next. Digests persist in a runtime-owned
// cache file, so a restart re-hashes only files that changed.
type installedStores struct {
	mu sync.Mutex
	// cache is the runtime-owned file the digests persist in; "" keeps them
	// in memory. loaded is true once it was read.
	cache  string
	loaded bool
	hashes map[string]installedHash
	// mismatched is the digest each file was last reported for, so one
	// changed file is reported once.
	mismatched map[string]string
	last       []router.HeldObject
	running    bool
	// reading counts the reading goroutine current started; wait waits for it.
	reading sync.WaitGroup
}

// wait returns once no reading current started is still running, so a
// digest cache save is never cut off mid-write.
func (h *installedStores) wait() { h.reading.Wait() }

// installedHashesFile is the runtime's digest cache beside its other state.
const installedHashesFile = "installed-hashes.json"

// installedCacheVersion is the version of installedHashesFile.
const installedCacheVersion = 1

type installedCache struct {
	Version int                      `json:"version"`
	Files   map[string]installedHash `json:"files"`
}

// fileBackedStores is each enabled declaration's store names that no
// inventory source serves and that its program may provide: the same
// abstraction.storage/inventory.provide decision an inventory source's store
// passes (acceptStores). writeStoreRules writes that permit for each
// declared store; an operator's deny withdraws the store's models.
func (p *runtimeProviders) fileBackedStores(ctx context.Context) []string {
	if p == nil || p.decide == nil {
		return nil
	}
	var declarations []providerDeclaration
	for _, s := range p.sorted() {
		if !s.file.Disabled {
			declarations = append(declarations, s.file.Declaration)
		}
	}
	return permittedFileStores(ctx, declarations, p.decide)
}

// permittedFileStores is fileBackedStores over declarations.
func permittedFileStores(ctx context.Context, declarations []providerDeclaration, decide func(ctx context.Context, program, action, resource string) bool) []string {
	var out []string
	for _, d := range declarations {
		if slices.Contains(d.Contracts, storageInventorySource) {
			continue
		}
		for _, store := range d.resources("store") {
			if !slices.Contains(out, store) && decide(ctx, d.Program, ActionInventoryProvide, ResourceStore(store)) {
				out = append(out, store)
			}
		}
	}
	return out
}

// current is the last finished reading of stores. It starts a new reading
// when none runs; one whose result differs calls changed, so the router is
// told without waiting for the clock.
func (h *installedStores) current(ctx context.Context, stores []string, report func(error), changed func()) []router.HeldObject {
	h.mu.Lock()
	last := slices.Clone(h.last)
	start := !h.running
	h.running = true
	h.mu.Unlock()
	if start {
		h.reading.Add(1)
		go func() {
			defer h.reading.Done()
			held := h.held(ctx, stores, report)
			h.mu.Lock()
			differs := ctx.Err() == nil && !reflect.DeepEqual(held, h.last)
			if differs {
				h.last = held
			}
			h.running = false
			h.mu.Unlock()
			if differs && changed != nil {
				changed()
			}
		}()
	}
	return last
}

// held reads every file-backed store and returns each listed file whose
// bytes match its record. A record that does not read, and a file that is
// missing, resized or changed, contribute nothing; a changed file is reported
// once for each new digest.
func (h *installedStores) held(ctx context.Context, stores []string, report func(error)) []router.HeldObject {
	h.load(report)
	var out []router.HeldObject
	seen := map[string]bool{}
	var folders []string
	changed := false
	if len(stores) > 0 {
		root, err := installedStoresRoot()
		if err != nil {
			report(fmt.Errorf("runtime installed stores: %w", err))
			return nil
		}
		for _, store := range stores {
			folder := filepath.Join(root, store)
			folders = append(folders, folder)
			record, err := readInstalledRecord(filepath.Join(folder, installedRecordName))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				report(fmt.Errorf("runtime installed store %s: %w", store, err))
				continue
			}
			for _, model := range record.Models {
				for _, file := range model.Files {
					if ctx.Err() != nil {
						return out
					}
					full := filepath.Join(folder, filepath.FromSlash(file.Path))
					seen[full] = true
					digest, fresh, err := h.digest(ctx, full, file.Size)
					changed = changed || fresh
					if err != nil {
						continue
					}
					if digest != file.SHA256 {
						h.mu.Lock()
						if h.mismatched == nil {
							h.mismatched = map[string]string{}
						}
						first := h.mismatched[full] != digest
						h.mismatched[full] = digest
						h.mu.Unlock()
						if first {
							report(fmt.Errorf("runtime installed store %s: %s does not match its recorded SHA-256; %s is withdrawn", store, file.Path, model.Model))
						}
						continue
					}
					out = append(out, router.HeldObject{Store: store, Names: []string{model.Model},
						Digest: "sha256:" + digest, Evidence: content.EvidenceHashed})
				}
			}
		}
	}
	// A store this reading did not read (one whose permit was withdrawn)
	// keeps its digests, so allowing it again re-hashes nothing.
	inRead := func(path string) bool {
		return slices.ContainsFunc(folders, func(folder string) bool {
			return strings.HasPrefix(path, folder+string(filepath.Separator))
		})
	}
	h.mu.Lock()
	for path := range h.hashes {
		if !seen[path] && inRead(path) {
			delete(h.hashes, path)
			changed = true
		}
	}
	for path := range h.mismatched {
		if !seen[path] {
			delete(h.mismatched, path)
		}
	}
	h.mu.Unlock()
	if changed {
		h.save(report)
	}
	return out
}

// load reads the persisted digests once. A cache that does not read starts
// empty: every file is hashed again.
func (h *installedStores) load(report func(error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.loaded {
		return
	}
	h.loaded = true
	if h.hashes == nil {
		h.hashes = map[string]installedHash{}
	}
	if h.cache == "" {
		return
	}
	raw, err := os.ReadFile(h.cache)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var saved installedCache
	if err == nil {
		err = json.Unmarshal(raw, &saved)
	}
	if err != nil || saved.Version != installedCacheVersion {
		report(fmt.Errorf("runtime installed stores: the digest cache does not read; its files are hashed again: %v", err))
		return
	}
	for path, entry := range saved.Files {
		if filepath.IsAbs(path) && installedDigest.MatchString(entry.Digest) {
			h.hashes[path] = entry
		}
	}
}

// save replaces the persisted digests in one rename.
func (h *installedStores) save(report func(error)) {
	h.mu.Lock()
	if h.cache == "" {
		h.mu.Unlock()
		return
	}
	data, err := json.Marshal(installedCache{Version: installedCacheVersion, Files: h.hashes})
	cache := h.cache
	h.mu.Unlock()
	if err == nil {
		err = writeInstalledCache(cache, data)
	}
	if err != nil {
		report(fmt.Errorf("runtime installed stores: the digest cache was not saved: %w", err))
	}
}

func writeInstalledCache(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return cas.Change(file, func([]byte) ([]byte, error) { return data, nil })
}

// readInstalledRecord reads and checks one installed.json: version 1, each
// model named, each path a clean relative slash path inside the folder, each
// size non-negative and each SHA-256 lowercase hex.
func readInstalledRecord(file string) (installedRecord, error) {
	f, err := os.Open(file)
	if err != nil {
		return installedRecord{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, installedRecordLimit+1))
	if err != nil {
		return installedRecord{}, err
	}
	if len(raw) > installedRecordLimit {
		return installedRecord{}, errors.New("installed.json exceeds 1 MiB")
	}
	var record installedRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return installedRecord{}, fmt.Errorf("installed.json: %w", err)
	}
	if record.Version != 1 {
		return installedRecord{}, fmt.Errorf("installed.json version %d", record.Version)
	}
	files := 0
	for _, model := range record.Models {
		if model.Model == "" || len(model.Model) > 256 || len(model.Files) == 0 {
			return installedRecord{}, errors.New("installed.json: a model needs a name and its files")
		}
		for _, file := range model.Files {
			files++
			if files > installedMaxFiles || !validInstalledPath(file.Path) || file.Size < 0 || !installedDigest.MatchString(file.SHA256) {
				return installedRecord{}, fmt.Errorf("installed.json: file %q", file.Path)
			}
		}
	}
	return record, nil
}

// validInstalledPath admits a clean relative slash path with no parent step.
func validInstalledPath(p string) bool {
	return p != "" && len(p) <= 1024 && path.Clean(p) == p && !path.IsAbs(p) && !strings.ContainsAny(p, `\:`) &&
		p != "." && !slices.Contains(strings.Split(p, "/"), "..")
}

// digest is the file's SHA-256 in lowercase hex: the remembered one while its
// size and write time are unchanged, otherwise a fresh hash (fresh true). A
// file that is not a regular file of size bytes has none.
func (h *installedStores) digest(ctx context.Context, file string, size int64) (string, bool, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return "", false, errors.New("installed file changed size")
	}
	h.mu.Lock()
	known, ok := h.hashes[file]
	h.mu.Unlock()
	if ok && known.Size == info.Size() && known.Modified.Equal(info.ModTime()) {
		return known.Digest, false, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, contextReader{ctx: ctx, r: f}); err != nil {
		return "", false, err
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	h.mu.Lock()
	if h.hashes == nil {
		h.hashes = map[string]installedHash{}
	}
	h.hashes[file] = installedHash{Size: info.Size(), Modified: info.ModTime(), Digest: digest}
	h.mu.Unlock()
	return digest, true, nil
}

// contextReader stops a long hash when its refresh ends.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
