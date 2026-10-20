package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	identity "github.com/openabstractions/abstraction-identity"
)

// An image identity refuses blocks the upgrade: the question could not be
// asked, so it fails closed (ID-F1).
func TestImageOnARefusedDriveBlocksTheUpgrade(t *testing.T) {
	restore := identity.TreatDriveAsNetwork('Y')
	defer restore()
	local := t.TempDir()
	for _, c := range [][2]string{
		{`Y:\OpenAbstractions\serve.exe`, `Y:\OpenAbstractions`},
		{`Y:\elsewhere\app.exe`, local},
		{filepath.Join(local, "serve.exe"), `Y:\OpenAbstractions`},
	} {
		if !insideFolder(c[0], c[1]) {
			t.Errorf("insideFolder(%q, %q) = false; a refused path must block", c[0], c[1])
		}
	}
	if insideFolder(filepath.Join(t.TempDir(), "app.exe"), local) {
		t.Error("a local image outside the folder blocked the upgrade")
	}
}

const (
	testFolder    = `C:\Users\oa-test\AppData\Local\Programs\OpenAbstractions`
	machineFolder = `C:\Program Files\OpenAbstractions`
)

type testExclusion struct {
	env       exclusionEnv
	dir       string
	alive     map[uint32]bool
	untrusted map[string]bool
}

func newTestExclusion(t *testing.T) *testExclusion {
	t.Helper()
	x := &testExclusion{dir: t.TempDir(), alive: map[uint32]bool{}, untrusted: map[string]bool{}}
	x.env = exclusionEnv{
		path: func(scope string) (string, error) { return filepath.Join(x.dir, scope, "exclusion.json"), nil },
		read: os.ReadFile,
		write: func(path, _ string, data []byte) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			return os.WriteFile(path, data, 0o600)
		},
		remove: os.Remove,
		trusted: func(path, _ string) error {
			if x.untrusted[path] {
				return errors.New("owned by another account")
			}
			return nil
		},
		alive: func(holder processIdentity, _ time.Time) bool { return x.alive[holder.PID] },
		now:   func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) },
	}
	return x
}

func holderFor(pid uint32) processIdentity {
	return processIdentity{PID: pid, Created: 1000 + int64(pid), Image: `C:\Windows\System32\msiexec.exe`}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestUpgradeExclusionRecordIsStrict(t *testing.T) {
	valid := upgradeExclusion{Version: 1, Scope: "user", Folders: []string{testFolder}, Installer: holderFor(7),
		Begun: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeExclusion(data)
	if err != nil || !reflect.DeepEqual(*decoded, valid) {
		t.Fatalf("round trip: %+v %v", decoded, err)
	}
	broken := map[string]func(*upgradeExclusion){
		"version":            func(r *upgradeExclusion) { r.Version = 2 },
		"scope":              func(r *upgradeExclusion) { r.Scope = "session" },
		"no folder":          func(r *upgradeExclusion) { r.Folders = nil },
		"relative folder":    func(r *upgradeExclusion) { r.Folders = []string{`Programs\OpenAbstractions`} },
		"volume root":        func(r *upgradeExclusion) { r.Folders = []string{`C:\`} },
		"unclean folder":     func(r *upgradeExclusion) { r.Folders = []string{`C:\Users\x\..\y`} },
		"no installer":       func(r *upgradeExclusion) { r.Installer.PID = 0 },
		"unreadable, no pid": func(r *upgradeExclusion) { r.Installer.PID, r.Installer.Created = 0, 0 },
		"negative creation":  func(r *upgradeExclusion) { r.Installer.Created = -1 },
		"no start time":      func(r *upgradeExclusion) { r.Begun = time.Time{} },
	}
	for name, mutate := range broken {
		copy := valid
		copy.Folders = append([]string(nil), valid.Folders...)
		mutate(&copy)
		data, _ := json.Marshal(copy)
		if _, err := decodeExclusion(data); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	text := string(data)
	for name, raw := range map[string]string{
		"unknown field": strings.Replace(text, `"version":1`, `"version":1,"extra":true`, 1),
		"stopped list":  strings.Replace(text, `"version":1`, `"version":1,"stopped":[]`, 1),
		"trailing data": text + "{}",
		"oversized":     text + strings.Repeat(" ", exclusionMaxBytes),
	} {
		if _, err := decodeExclusion([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	// The record is the 0.1.7 record without its stopped and services lists,
	// which a 0.1.7 reader treats as empty.
	var fields map[string]any
	json.Unmarshal(data, &fields)
	if _, ok := fields["stopped"]; ok || len(fields) != 5 {
		t.Fatalf("record fields %v", fields)
	}
}

func TestActivationHonoursOnlyLiveCoveringTrustedRecords(t *testing.T) {
	x := newTestExclusion(t)
	x.alive[7] = true
	if err := x.env.begin("user", []string{testFolder}, holderFor(7)); err != nil {
		t.Fatal(err)
	}
	inside := testFolder + `\tools\openabstractionsw.exe`
	if err := x.env.refusal(inside); !errors.Is(err, errUpgradeInProgress) || !strings.Contains(err.Error(), "installer process 7") {
		t.Fatalf("live covering record did not refuse: %v", err)
	}
	var exit *exitError
	if err := upgradeRefusal(x.env.refusal(inside)); !errors.As(err, &exit) || exit.code != 3 {
		t.Fatalf("refusal does not carry exit status 3: %v", err)
	}
	if err := x.env.refusal(`D:\Other\OpenAbstractions\tools\openabstractionsw.exe`); err != nil {
		t.Fatalf("record refused another installation: %v", err)
	}
	x.alive[7] = false
	if record, notes := x.env.active(inside); record != nil || len(notes) != 1 || !strings.Contains(notes[0], "is gone") {
		t.Fatalf("stale holder excluded: %+v %v", record, notes)
	}
	x.alive[7] = true
	path, _ := x.env.path("user")
	x.untrusted[path] = true
	if record, notes := x.env.active(inside); record != nil || len(notes) != 1 || !strings.Contains(notes[0], "owned by another account") {
		t.Fatalf("untrusted record excluded: %+v %v", record, notes)
	}
	delete(x.untrusted, path)
	if err := os.WriteFile(path, []byte(`{"version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if record, notes := x.env.active(inside); record != nil || len(notes) != 1 {
		t.Fatalf("malformed record excluded: %+v %v", record, notes)
	}
	x.alive[9] = true
	if err := x.env.begin("machine", []string{machineFolder}, holderFor(9)); err != nil {
		t.Fatal(err)
	}
	if err := x.env.refusal(machineFolder + `\tools\openabstractions.exe`); !errors.Is(err, errUpgradeInProgress) {
		t.Fatalf("machine record did not refuse its installation: %v", err)
	}
}

func TestBeginRefusesAnotherLiveInstaller(t *testing.T) {
	x := newTestExclusion(t)
	x.alive[7], x.alive[8] = true, true
	if err := x.env.begin("user", []string{testFolder}, holderFor(7)); err != nil {
		t.Fatal(err)
	}
	if err := x.env.begin("user", []string{testFolder}, holderFor(8)); err == nil || !strings.Contains(err.Error(), "installer process 7") {
		t.Fatalf("second live installer accepted: %v", err)
	}
	if err := x.env.begin("user", []string{testFolder}, holderFor(7)); err != nil {
		t.Fatalf("same installer could not rewrite its record: %v", err)
	}
	x.alive[7] = false
	if err := x.env.begin("user", []string{testFolder}, holderFor(8)); err != nil {
		t.Fatalf("stale record blocked a new installer: %v", err)
	}
	record, _, err := x.env.load("user")
	if err != nil || record.Installer != holderFor(8) {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestBeginUpgradeChainKeepsBurnHolderAndV1Record(t *testing.T) {
	x := newTestExclusion(t)
	x.alive[7], x.alive[8] = true, true
	token := strings.Repeat("a", 32)
	if err := x.env.beginContext(t.Context(), "machine", []string{machineFolder}, holderFor(7), token); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "custom")
	if err := x.env.beginContext(t.Context(), "machine", []string{machineFolder, custom}, holderFor(8), token); err != nil {
		t.Fatal(err)
	}
	path, _ := x.env.path("machine")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodeExclusion(data)
	if err != nil || record.Installer != holderFor(7) || !reflect.DeepEqual(record.Folders, []string{machineFolder, custom}) {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if notes := x.env.release("machine"); len(notes) != 0 {
		t.Fatal(notes)
	}
	for _, name := range []string{path, chainPath(path)} {
		if fileExists(name) {
			t.Fatalf("release retained %s", name)
		}
	}
}

func TestBeginUpgradeChainRefusesForeignOrUntrustedAdmission(t *testing.T) {
	for _, fault := range []string{"missing-token", "wrong-token", "scope", "holder", "untrusted", "missing-chain", "unknown-field", "interrupted-before-exclusion"} {
		t.Run(fault, func(t *testing.T) {
			x := newTestExclusion(t)
			x.alive[7], x.alive[8] = true, true
			token := strings.Repeat("a", 32)
			if err := x.env.beginContext(t.Context(), "machine", []string{machineFolder}, holderFor(7), token); err != nil {
				t.Fatal(err)
			}
			path, _ := x.env.path("machine")
			sidecar := chainPath(path)
			chain := upgradeChain{Version: 1, Scope: "machine", Token: token, Holder: holderFor(7)}
			switch fault {
			case "missing-token":
				token = ""
			case "wrong-token":
				token = strings.Repeat("b", 32)
			case "scope":
				chain.Scope = "user"
			case "holder":
				chain.Holder = holderFor(9)
			case "untrusted":
				x.untrusted[sidecar] = true
			case "missing-chain":
				if err := os.Remove(sidecar); err != nil {
					t.Fatal(err)
				}
			case "interrupted-before-exclusion":
				chain.Holder = holderFor(8)
			}
			if fault != "missing-chain" {
				data, _ := json.Marshal(chain)
				if fault == "unknown-field" {
					data = append(data[:len(data)-1], []byte(`,"extra":true}`)...)
				}
				if err := os.WriteFile(sidecar, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := x.env.beginContext(t.Context(), "machine", []string{machineFolder}, holderFor(8), token); err == nil || !strings.Contains(err.Error(), "installer process 7") {
				t.Fatalf("foreign admission: %v", err)
			}
			record, _, _ := x.env.load("machine")
			if record.Installer != holderFor(7) {
				t.Fatal("foreign holder replaced")
			}
		})
	}
}

func TestBeginUpgradeChainInterruptedWritesRemainRecoverable(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		for _, successor := range []uint32{7, 8} {
			t.Run(fmt.Sprintf("cut%d/holder%d", failAt, successor), func(t *testing.T) {
				x := newTestExclusion(t)
				x.alive[7], x.alive[8] = true, true
				write := x.env.write
				writes := 0
				x.env.write = func(path, scope string, data []byte) error {
					writes++
					if writes == failAt {
						return errors.New("inert write interruption")
					}
					if writes == 1 && filepath.Base(path) != "chain.json" {
						t.Fatal("v1 written before chain")
					}
					return write(path, scope, data)
				}
				token := strings.Repeat("a", 32)
				if err := x.env.beginContext(t.Context(), "machine", []string{machineFolder}, holderFor(7), token); err == nil {
					t.Fatal("write interruption ignored")
				}
				path, _ := x.env.path("machine")
				if fileExists(path) {
					t.Fatal("interrupted admission wrote exclusion")
				}
				x.env.write = write
				if successor == 8 {
					token = strings.Repeat("b", 32)
				}
				if err := x.env.beginContext(t.Context(), "machine", []string{machineFolder}, holderFor(successor), token); err != nil {
					t.Fatal(err)
				}
				if !x.env.matchingChain(path, "machine", token, holderFor(successor)) {
					t.Fatal("retry lost chain")
				}
				if record, _, err := x.env.load("machine"); err != nil || record.Installer != holderFor(successor) {
					t.Fatalf("record=%+v err=%v", record, err)
				}
			})
		}
	}
}

func TestBeginUpgradeChainSerializesContendersBeforeReading(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			x := newTestExclusion(t)
			x.env.lock = lockUpgradeScope
			x.alive[7], x.alive[8], x.alive[9] = true, true, true
			tokenA, tokenB := strings.Repeat("a", 32), strings.Repeat("b", 32)
			paused, proceed, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			resume := sync.OnceFunc(func() { close(proceed) })
			write := x.env.write
			x.env.write = func(path, scope string, data []byte) error {
				if err := write(path, scope, data); err != nil {
					return err
				}
				if filepath.Base(path) == "chain.json" {
					var chain upgradeChain
					if err := json.Unmarshal(data, &chain); err != nil {
						return err
					}
					if chain.Token == tokenA {
						close(paused)
						<-proceed
					}
				}
				return nil
			}
			var firstErr error
			go func() {
				defer close(finished)
				firstErr = x.env.beginContext(t.Context(), "user", []string{testFolder}, holderFor(7), tokenA)
			}()
			defer func() {
				resume()
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("first begin did not join")
				}
			}()
			select {
			case <-paused:
			case <-time.After(3 * time.Second):
				t.Fatal("first chain was not written")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			var err error
			if mode == "cancel" {
				ctx, cancel = context.WithCancel(t.Context())
				defer cancel()
				attempted, done := make(chan struct{}), make(chan struct{})
				signal := sync.OnceFunc(func() { close(attempted) })
				x.env.lock = func(ctx context.Context, path, scope string) (func(), error) {
					signal()
					return lockUpgradeScope(ctx, path, scope)
				}
				go func() {
					defer close(done)
					err = x.env.beginContext(ctx, "user", []string{testFolder}, holderFor(8), tokenB)
				}()
				defer func() {
					cancel()
					resume()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("cancelled contender did not join")
					}
				}()
				select {
				case <-attempted:
				case <-time.After(3 * time.Second):
					t.Fatal("contender did not try the scope lock")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("scope wait ignored cancellation")
				}
			} else {
				err = x.env.beginContext(ctx, "user", []string{testFolder}, holderFor(8), tokenB)
			}
			want := context.DeadlineExceeded
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("contender entered incomplete admission: %v", err)
			}
			path, _ := x.env.path("user")
			if fileExists(path) || !x.env.matchingChain(path, "user", tokenA, holderFor(7)) {
				t.Fatal("contender changed the first admission")
			}
			resume()
			<-finished
			if firstErr != nil {
				t.Fatal(firstErr)
			}
			if err := x.env.beginContext(t.Context(), "user", []string{testFolder}, holderFor(8), tokenB); err == nil || !strings.Contains(err.Error(), "installer process 7") {
				t.Fatalf("foreign contender accepted: %v", err)
			}
			if err := x.env.beginContext(t.Context(), "user", []string{testFolder}, holderFor(9), tokenA); err != nil {
				t.Fatalf("winner lost its nested admission: %v", err)
			}
			record, _, err := x.env.load("user")
			if err != nil || record.Installer != holderFor(7) {
				t.Fatalf("winner=%+v err=%v", record, err)
			}
		})
	}
}

func TestUpgradeChainReleaseSerializesWithNewAdmission(t *testing.T) {
	x := newTestExclusion(t)
	x.env.lock = lockUpgradeScope
	x.alive[7], x.alive[8] = true, true
	tokenA, tokenB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	if err := x.env.beginContext(t.Context(), "user", []string{testFolder}, holderFor(7), tokenA); err != nil {
		t.Fatal(err)
	}
	path, _ := x.env.path("user")
	paused, proceed, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(proceed) })
	remove := x.env.remove
	x.env.remove = func(name string) error {
		err := remove(name)
		if name == path && err == nil {
			close(paused)
			<-proceed
		}
		return err
	}
	var releaseErr error
	go func() { defer close(finished); _, releaseErr = x.env.removeContext(t.Context(), "user") }()
	defer func() {
		resume()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("release did not join")
		}
	}()
	select {
	case <-paused:
	case <-time.After(3 * time.Second):
		t.Fatal("release did not reach the record cut")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if err := x.env.beginContext(ctx, "user", []string{testFolder}, holderFor(8), tokenB); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission passed incomplete release: %v", err)
	}
	if !x.env.matchingChain(path, "user", tokenA, holderFor(7)) {
		t.Fatal("release waiter changed the old sidecar")
	}
	resume()
	<-finished
	if releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if err := x.env.beginContext(t.Context(), "user", []string{testFolder}, holderFor(8), tokenB); err != nil {
		t.Fatal(err)
	}
	if !x.env.matchingChain(path, "user", tokenB, holderFor(8)) {
		t.Fatal("old release erased the new sidecar")
	}
}

func TestUpgradeChainFailedRecordRemovalKeepsNestedAdmission(t *testing.T) {
	x := newTestExclusion(t)
	x.env.lock = lockUpgradeScope
	x.alive[7], x.alive[8] = true, true
	token := strings.Repeat("a", 32)
	if err := x.env.beginContext(t.Context(), "user", []string{testFolder}, holderFor(7), token); err != nil {
		t.Fatal(err)
	}
	path, _ := x.env.path("user")
	remove := x.env.remove
	x.env.remove = func(name string) error {
		if name == path {
			return errors.New("inert removal refusal")
		}
		return remove(name)
	}
	if _, err := x.env.removeContext(t.Context(), "user"); err == nil {
		t.Fatal("removal refusal ignored")
	}
	if !x.env.matchingChain(path, "user", token, holderFor(7)) {
		t.Fatal("failed guard removal destroyed its sidecar")
	}
	if err := x.env.beginContext(t.Context(), "user", []string{testFolder}, holderFor(8), token); err != nil {
		t.Fatal(err)
	}
}

func TestBeginRefusesAnotherLiveUnreadableHolder(t *testing.T) {
	x := newTestExclusion(t)
	first := processIdentity{PID: 7, Image: "msiexec.exe"}
	second := processIdentity{PID: 8, Image: "msiexec.exe"}
	x.alive[7], x.alive[8] = true, true
	if err := x.env.begin("user", []string{testFolder}, first); err != nil {
		t.Fatal(err)
	}
	if err := x.env.begin("user", []string{testFolder}, second); err == nil || !strings.Contains(err.Error(), "installer process 7") {
		t.Fatalf("second live installer accepted: %v", err)
	}
	if err := x.env.begin("user", []string{testFolder}, first); err != nil {
		t.Fatalf("same unreadable installer could not rewrite its record: %v", err)
	}
}

func TestReleaseRemovesTheRecordAndNamesAMissingOne(t *testing.T) {
	x := newTestExclusion(t)
	x.alive[7] = true
	if err := x.env.begin("user", []string{testFolder}, holderFor(7)); err != nil {
		t.Fatal(err)
	}
	path, _ := x.env.path("user")
	if notes := x.env.release("user"); len(notes) != 0 || fileExists(path) {
		t.Fatalf("release: %v exists=%v", notes, fileExists(path))
	}
	if notes := x.env.release("user"); len(notes) != 1 || !strings.Contains(notes[0], "no user upgrade exclusion was held") {
		t.Fatalf("second release: %v", notes)
	}
	if err := x.env.refusal(testFolder + `\tools\openabstractionsw.exe`); err != nil {
		t.Fatalf("released record still refuses: %v", err)
	}
}

// Real files, owners and process identities on this machine. No installer,
// service or other account is involved.
func TestExclusionOwnerUsesNativeSIDEquality(t *testing.T) {
	if err := trustedExclusionOwner(nil, "user"); err == nil {
		t.Fatal("missing native owner accepted")
	}
	account, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := windows.StringToSid(account)
	if err != nil {
		t.Fatal(err)
	}
	if err := trustedExclusionOwner(owner, "user"); err != nil {
		t.Fatalf("current native account refused: %v", err)
	}
	foreign, err := windows.CreateWellKnownSid(windows.WinAnonymousSid)
	if err != nil {
		t.Fatal(err)
	}
	if windows.EqualSid(owner, foreign) {
		t.Fatal("fixture account unexpectedly anonymous")
	}
	if err := trustedExclusionOwner(foreign, "user"); err == nil {
		t.Fatal("foreign native account accepted")
	}
}

func TestExclusionFilesAndIdentitiesOnThisMachine(t *testing.T) {
	if _, err := windows.SecurityDescriptorFromString(machineUpgradeSDDL); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "user", "exclusion.json")
	record := upgradeExclusion{Version: 1, Scope: "user", Folders: []string{testFolder}, Installer: holderFor(7), Begun: time.Now().UTC()}
	data, _ := json.Marshal(record)
	for i := 0; i < 2; i++ { // the second write replaces the first
		if err := writeExclusionFile(path, "user", data); err != nil {
			t.Fatal(err)
		}
	}
	read, err := readExclusionFile(path)
	if err != nil || string(read) != string(data) {
		t.Fatalf("read %q err=%v", read, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "exclusion-*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
	if _, err := readExclusionFile(filepath.Dir(path)); err == nil {
		t.Fatal("a directory was read as a record")
	}
	if err := trustedExclusionFile(path, "user"); err != nil {
		t.Fatalf("own user record untrusted: %v", err)
	}
	owner, err := pathOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	if !administrativeSID(owner) {
		if err := trustedExclusionFile(path, "machine"); err == nil {
			t.Fatal("a standard user's file was trusted as a machine record")
		}
	}
	own, err := handleCreated(windows.CurrentProcess())
	if err != nil {
		t.Fatal(err)
	}
	self := processIdentity{PID: uint32(os.Getpid()), Created: own}
	now := time.Now()
	if !processAlive(self, now, now) {
		t.Fatal("this process is not alive")
	}
	if processAlive(processIdentity{PID: self.PID, Created: own + 1}, now, now) {
		t.Fatal("a different creation time named this process")
	}
	if processAlive(processIdentity{PID: 0xFFFFFFF1, Created: own}, now, now) {
		t.Fatal("an invalid PID is alive")
	}
	parent, err := installerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if parent.Created > own || !filepath.IsAbs(parent.Image) || !processAlive(parent, now, now) {
		t.Fatalf("parent identity %+v", parent)
	}
}

func TestUpgradeServiceArguments(t *testing.T) {
	codeA := "{2692D2EB-EC1F-443C-BC91-4C654895184C}"
	for _, tc := range []struct {
		args []string
		want serviceRequest
	}{
		{[]string{"begin-upgrade", "--user", testFolder + `\.`, "--related", codeA}, serviceRequest{command: "begin-upgrade", scope: "user", folder: testFolder + `\.`, related: codeA}},
		{[]string{"begin-upgrade", "--machine", machineFolder, "--related", codeA}, serviceRequest{command: "begin-upgrade", scope: "machine", folder: machineFolder, related: codeA}},
		{[]string{"begin-removal", "--user", testFolder + `\.`}, serviceRequest{command: "begin-removal", scope: "user", folder: testFolder + `\.`}},
		{[]string{"begin-removal", "--machine", machineFolder}, serviceRequest{command: "begin-removal", scope: "machine", folder: machineFolder}},
		{[]string{"end-upgrade", "--user"}, serviceRequest{command: "end-upgrade", scope: "user"}},
		{[]string{"end-upgrade", "--machine"}, serviceRequest{command: "end-upgrade", scope: "machine"}},
		{[]string{"upgrade-check"}, serviceRequest{command: "upgrade-check"}},
		{[]string{"start", "--machine"}, serviceRequest{command: "start", scope: "machine"}},
		{[]string{"start", "--related", codeA}, serviceRequest{command: "start", scope: "user", related: codeA}},
		{[]string{"start", "--user", testFolder, "0.8.0"}, serviceRequest{command: "start", scope: "user", folder: testFolder, version: "0.8.0"}},
		{[]string{"remove-sandboxes"}, serviceRequest{command: "remove-sandboxes", scope: "user"}},
	} {
		got, err := serviceArguments(tc.args)
		if err != nil || got != tc.want {
			t.Fatalf("%v: got %+v err=%v", tc.args, got, err)
		}
	}
	for _, args := range [][]string{
		nil, {"stop"}, {"stop", "--user", testFolder}, {"install"}, {"uninstall"}, {"run"},
		{"begin-upgrade", "--user", testFolder}, {"begin-upgrade", "--session", testFolder, "--related", codeA},
		{"begin-upgrade", "--user", "", "--related", codeA}, {"begin-upgrade", "--user", testFolder, "--related", ""},
		{"begin-removal"}, {"begin-removal", "--user", ""}, {"begin-removal", "--session", testFolder},
		{"begin-removal", "--machine", machineFolder, "--related", codeA},
		{"end-upgrade"}, {"end-upgrade", "--user", "extra"}, {"upgrade-check", "--user"},
		{"start"}, {"start", "--runtime"}, {"start", "--related"}, {"start", "--related", ""},
		{"start", "--machine", "extra"}, {"start", "--user"}, {"start", "--related", codeA, "--user", testFolder},
		{"remove-sandboxes", "--user"}, {"remove-sandboxes", "extra"},
	} {
		if _, err := serviceArguments(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
