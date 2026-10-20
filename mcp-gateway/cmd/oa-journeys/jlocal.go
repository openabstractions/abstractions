//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"golang.org/x/sys/windows"
)

// The cold journeys (J-COLD-CHAT, J-PIC-COLD) host a real chat model on the
// real model host of the build under test (-tools\modelhostd.exe), as Setup
// does: the llama.cpp engine staged from the bundle ZIP the package ships,
// an inventory source over a model folder, and "models host". The model
// folder holds a hard link to the GGUF the person already has (-chat-model);
// nothing is copied and the person's file is only read.

// gpuModelLimit is how much graphics memory a model-loading process may
// hold before a journey refuses to load a model; gpuOtherLimit is the same
// for any other process that is not a browser or the compositor.
const (
	gpuModelLimit = 2 << 30
	gpuOtherLimit = 4 << 30
)

// gpuQuiet lists the processes that hold graphics memory for drawing, never
// for a model: the desktop compositor, which counts every window's
// surfaces, and the browsers.
var gpuQuiet = map[string]bool{"dwm.exe": true, "csrss.exe": true, "msedge.exe": true, "msedgewebview2.exe": true, "chrome.exe": true, "firefox.exe": true}

// gpuModelLoader is whether a process image name is one that loads models:
// Python, the llama.cpp and stable-diffusion engines, OA's model host and
// PyTorch service, and the other local engines.
func gpuModelLoader(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	for _, prefix := range []string{"python", "llama", "modelhost", "pytorchd", "sd", "stable-diffusion", "ollama", "lemonade", "koboldcpp", "lms", "comfyui", "whisper", "vllm"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// gpuHolderLimit is the graphics memory the process name may hold before it
// blocks a journey, and false for a process that never blocks one.
func gpuHolderLimit(name string) (int64, bool) {
	switch {
	case gpuQuiet[strings.ToLower(name)]:
		return 0, false
	case gpuModelLoader(name):
		return gpuModelLimit, true
	}
	return gpuOtherLimit, true
}

// gpuHolders names every process other than this run's own that holds more
// than its gpuHolderLimit of graphics memory, from the GPU Process Memory
// counters. "" means the card is free for one short run. It logs every
// process above 1 GiB and whether it blocks.
func gpuHolders(ctx context.Context, own func(int) bool, logf func(string, ...any)) (string, error) {
	script := `$ErrorActionPreference='Stop'; (Get-Counter '\GPU Process Memory(*)\Local Usage','\GPU Process Memory(*)\Shared Usage').CounterSamples | ForEach-Object { "$($_.InstanceName) $([int64]$_.CookedValue)" }`
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := childproc.Hidden(exec.CommandContext(cctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)).Output()
	if err != nil {
		return "", fmt.Errorf("read the GPU memory counters: %w", err)
	}
	return gpuBusy(parseGPUCounters(string(out)), processNames(), own, logf), nil
}

// parseGPUCounters sums the counter lines per process ID.
func parseGPUCounters(out string) map[int]int64 {
	used := map[int]int64{}
	pidOf := regexp.MustCompile(`^pid_(\d+)_`)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		m := pidOf.FindStringSubmatch(fields[0])
		bytes, err := strconv.ParseInt(fields[1], 10, 64)
		if m == nil || err != nil {
			continue
		}
		pid, _ := strconv.Atoi(m[1])
		used[pid] += bytes
	}
	return used
}

// gpuBusy applies the limits to used and returns the blocking holders.
func gpuBusy(used map[int]int64, names map[int]string, own func(int) bool, logf func(string, ...any)) string {
	pids := make([]int, 0, len(used))
	for pid := range used {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	var busy, seen []string
	for _, pid := range pids {
		bytes, name := used[pid], firstNonEmpty(names[pid], "?")
		if bytes < 1<<30 || (own != nil && own(pid)) {
			continue
		}
		line := fmt.Sprintf("%s (pid %d) holds %.1f GiB", name, pid, float64(bytes)/(1<<30))
		limit, counts := gpuHolderLimit(name)
		switch {
		case !counts:
			seen = append(seen, line+", skipped")
		case bytes < limit:
			seen = append(seen, fmt.Sprintf("%s, under %d GiB", line, limit>>30))
		default:
			seen = append(seen, line+", blocks")
			busy = append(busy, line)
		}
	}
	if logf != nil {
		logf("GPU memory holders: %s", firstNonEmpty(strings.Join(seen, "; "), "none above 1 GiB"))
	}
	return strings.Join(busy, "; ")
}

// processNames maps running process IDs to their image names.
func processNames() map[int]string {
	names := map[int]string{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return names
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		names[int(entry.ProcessID)] = windows.UTF16ToString(entry.ExeFile[:])
	}
	return names
}

// localChat is the real chat model hosted in one isolated runtime.
type localChat struct {
	file  string // the hard link the inventory reads
	bytes int64
	store string
	host  string // models host output
}

var engineOnce sync.Mutex

// stageEngine installs the llama.cpp engine from the bundle once per run
// and returns llama-server.exe and the model host state it was recorded in.
func stageEngine(ctx context.Context, o *options) (engine, state string, err error) {
	engineOnce.Lock()
	defer engineOnce.Unlock()
	cache := filepath.Join(filepath.Dir(o.work), "cache")
	dir, state := filepath.Join(cache, "engine"), filepath.Join(cache, "modelhost-state")
	if found, _ := filepath.Glob(filepath.Join(dir, "*", "llama-server.exe")); len(found) == 1 {
		return found[0], state, nil
	}
	if found, _ := filepath.Glob(filepath.Join(dir, "llama-server.exe")); len(found) == 1 {
		return found[0], state, nil
	}
	bundle := o.engineBundle
	if bundle == "" {
		zips, _ := filepath.Glob(filepath.Join(o.tools, "engines", "llama-*.zip"))
		if len(zips) != 1 {
			return "", "", fmt.Errorf("no single llama.cpp bundle in %s (pass -engine-bundle): %v", filepath.Join(o.tools, "engines"), zips)
		}
		bundle = zips[0]
	}
	// The model host refuses a state folder another account can write.
	for _, folder := range []string{cache, state} {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			return "", "", err
		}
		if err := privatedir.Tighten(folder); err != nil {
			return "", "", fmt.Errorf("make %s private: %w", folder, err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := childproc.Hidden(exec.CommandContext(cctx, filepath.Join(o.tools, "modelhostd.exe"), "engine", "install", "--bundle", bundle, "--dir", dir, "--state", state)).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("modelhostd engine install: %v %s", err, strings.TrimSpace(string(out)))
	}
	m := regexp.MustCompile(`([A-Za-z]:[^\r\n]*llama-server\.exe)`).FindStringSubmatch(string(out))
	if m == nil {
		return "", "", fmt.Errorf("modelhostd engine install named no engine: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(m[1]), state, nil
}

// hostLocalChat declares the inventory source and the hosted model in e,
// as Setup's ModelHost steps do. The model is not loaded until asked for.
func hostLocalChat(ctx context.Context, e *env, o *options) (*localChat, error) {
	if o.chatModel == "" {
		return nil, errors.New("no chat model on this computer was named (journeys.py --chat-model)")
	}
	info, err := os.Stat(o.chatModel)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("chat model %s: %v", o.chatModel, err)
	}
	engine, state, err := stageEngine(ctx, o)
	if err != nil {
		return nil, err
	}
	folder := filepath.Join(filepath.Dir(o.work), "cache", "models")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, err
	}
	link := filepath.Join(folder, strings.ToLower(filepath.Base(o.chatModel)))
	if err := linkJourneyChatModel(o.chatModel, link); err != nil {
		return nil, err
	}
	// models host names a file named by its digest from the model's own
	// metadata.
	c := &localChat{file: link, bytes: info.Size(), store: "jrn-chat"}
	config := filepath.Join(e.work, "chat-inventory.json")
	raw, _ := json.Marshal(map[string]any{"extra_roots": []map[string]string{{"store": c.store, "path": folder, "holder": "", "expected_file_key": ""}}})
	if err := os.WriteFile(config, raw, 0o600); err != nil {
		return nil, err
	}
	out, err := e.cli(ctx, "provider", "add", c.store, "--program", filepath.Join(o.tools, "inventoryd.exe"), "--provider-endpoint", c.store+"-"+newID()[:6],
		"--contract", "abstraction.storage/inventory-source@1", "--store", c.store,
		"--arg", "serve", "--arg", "--endpoint", "--arg", "{endpoint}", "--arg", "--config", "--arg", config,
		"--arg", "--config-roots-only", "--arg", "--ignore-environment-roots")
	if err != nil {
		return nil, fmt.Errorf("inventory source: %v %s", err, strings.TrimSpace(out))
	}
	// The inventory finds the file on its first walk; models host resolves
	// the locator through it.
	var last string
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(2 * time.Second) {
		args := []string{"models", "host", c.store + "/" + filepath.Base(link), "--engine", engine, "--state", state,
			"--program", filepath.Join(o.tools, "modelhostd.exe"), "--json", "--timeout", "60s"}
		out, err = e.cli(ctx, args...)
		last = fmt.Sprintf("%v %s", err, strings.TrimSpace(out))
		if err == nil {
			c.host = strings.TrimSpace(out)
			e.logf("models host: %s", clip(c.host, 600))
			return c, nil
		}
		e.logf("models host: %s", clip(last, 400))
	}
	return nil, fmt.Errorf("models host: %s", clip(last, 600))
}

// linkJourneyChatModel reuses the cache member only when it is a hard link
// to the selected model. The member's inventory spelling supplies no identity.
func linkJourneyChatModel(source, link string) error {
	entry, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Link(source, link); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("hard-link %s into the journey's model folder (it must be on the work folder's volume; nothing is copied): %w", source, err)
		}
		entry, err = os.Lstat(link)
	}
	if err != nil {
		return err
	}
	if !entry.Mode().IsRegular() {
		return fmt.Errorf("journey model is not an ordinary file: %s", link)
	}
	same, err := fileid.SameFile(link, source)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("journey model %s does not match the selected file %s", link, source)
	}
	return nil
}

// coldLoadBudget is how long one cold load of size bytes may take: the file
// read at 300 MB/s, the engine's start and a minute for the card.
func coldLoadBudget(size int64) time.Duration {
	return time.Duration(size/300_000_000)*time.Second + 90*time.Second
}

// chatModelSize is the -chat-model file's size, 0 when unreadable.
func chatModelSize(o *options) int64 {
	if info, err := os.Stat(o.chatModel); err == nil {
		return info.Size()
	}
	return 0
}

// localChatName is the identifier the agent asks for the hosted model by:
// the servable chat model oa_models_list says store holds, by its first
// alias, else its family.
func localChatName(ctx context.Context, s *session, e *env, store string) (string, string) {
	var listed string
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(2 * time.Second) {
		_, text, structured, err := s.call(ctx, "oa_models_list", map[string]any{})
		listed = firstNonEmpty(text, errString(err))
		if permissionPending(text, structured) {
			e.answers.waitIDs(ctx, questionIDs(structured), 60*time.Second)
			continue
		}
		raw, _ := json.Marshal(structured["models"])
		var models []struct {
			Family   string   `json:"family"`
			Aliases  []string `json:"aliases"`
			Profiles []string `json:"profiles"`
			Servable bool     `json:"servable"`
			HeldIn   []string `json:"held_in"`
		}
		_ = json.Unmarshal(raw, &models)
		for _, m := range models {
			held := false
			for _, h := range m.HeldIn {
				held = held || strings.Contains(h, store)
			}
			if !m.Servable || !held {
				continue
			}
			if len(m.Aliases) > 0 {
				return m.Aliases[0], listed
			}
			return m.Family, listed
		}
	}
	return "", listed
}

// cardLease reports the selected program's lease on card:0 and the CLI report.
func cardLease(ctx context.Context, e *env, program string) (lease string, detail string) {
	out, err := e.cli(ctx, "resources", "card:0", "--json")
	if err != nil {
		return "", fmt.Sprintf("%v %s", err, clip(out, 300))
	}
	return reportedCardLease(out, program), clip(strings.TrimSpace(out), 500)
}

func reportedCardLease(raw, program string) string {
	var report struct {
		Resources []struct {
			Resource string
			Holders  []struct {
				Program string
				Lease   string
			}
		}
	}
	if json.Unmarshal([]byte(raw), &report) != nil {
		return ""
	}
	for _, resource := range report.Resources {
		if resource.Resource != "card:0" {
			continue
		}
		for _, holder := range resource.Holders {
			if holder.Lease != "" && sameJourneyProgram(holder.Program, program) {
				return holder.Lease
			}
		}
	}
	return ""
}

// removeLocalCache removes the run's model cache once every journey ended:
// first the hard links to the person's model file (the file itself stays),
// then the staged engine, whose files the model host leaves read-only to
// this account (this account gets full control of them back), then the
// rest.
func removeLocalCache(work string, logf func(string, ...any)) {
	cache := filepath.Join(work, "cache")
	if _, err := os.Stat(cache); err != nil {
		return
	}
	links, _ := filepath.Glob(filepath.Join(cache, "models", "*"))
	for _, link := range links {
		if err := os.Remove(link); err != nil {
			logf("cache: %s stays: %v", link, err)
			return
		}
	}
	if engine := filepath.Join(cache, "engine"); dirExists(engine) {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			logf("cache: %v", err)
			return
		}
		grant := "*" + user.User.Sid.String() + ":(OI)(CI)F"
		if out, err := childproc.Hidden(exec.Command("icacls", engine, "/grant", grant, "/T", "/C", "/Q")).CombinedOutput(); err != nil {
			logf("cache: open %s to removal: %v %s", engine, err, strings.TrimSpace(string(out)))
		}
	}
	if err := os.RemoveAll(cache); err != nil {
		logf("cache: %v", err)
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
