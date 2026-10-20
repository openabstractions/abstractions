package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeLemonade stands for the installed provider's package verbs. selected
// is the version its selection binds; activation moves it and copies the
// backend once.
type fakeLemonade struct {
	t        *testing.T
	calls    [][]string
	selected string
	copies   int
	// pairing is the backend archive and model revision package check
	// reports; empty reports the pin's.
	backend, model string
	checkCode      int
	activateReply  []byte
	activateErr    error
	// activateRefuses, when set, limits activateErr to that version.
	activateRefuses string
	retireCode      int
}

func (l *fakeLemonade) install(t *testing.T) {
	t.Helper()
	saved := lemonadeCLI
	lemonadeCLI = l.run
	t.Cleanup(func() { lemonadeCLI = saved })
}

func (l *fakeLemonade) verb(name string) [][]string {
	var calls [][]string
	for _, call := range l.calls {
		if len(call) > 1 && call[0] == "package" && call[1] == name {
			calls = append(calls, call)
		}
	}
	return calls
}

func flagValue(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func (l *fakeLemonade) run(_ context.Context, args ...string) ([]byte, error) {
	l.calls = append(l.calls, args)
	if len(args) < 2 || args[0] != "package" {
		return nil, errors.New("unexpected provider command")
	}
	version := flagValue(args, "--version")
	switch args[1] {
	case "check":
		if l.checkCode != 0 {
			return nil, &lemonadeExit{Code: l.checkCode, Reason: "package differs from the reviewed manifest"}
		}
		return json.Marshal(map[string]any{"version": 1, "ok": true, "backend_archive_sha256": l.backend, "model_revision": l.model})
	case "activate":
		if l.activateErr != nil && (l.activateRefuses == "" || l.activateRefuses == version) {
			return nil, l.activateErr
		}
		if l.selected != version {
			l.copies++
			l.selected = version
		}
		if l.activateReply != nil {
			return l.activateReply, nil
		}
		return json.Marshal(map[string]any{"version": 1, "ok": true, "active_version": version})
	case "retire":
		if l.retireCode != 0 {
			return nil, &lemonadeExit{Code: l.retireCode, Reason: "lemonade setup: held"}
		}
		if version == l.selected {
			return nil, &lemonadeExit{Code: 2, Reason: "the selected version"}
		}
		return []byte(`{"version":1,"ok":true}` + "\n"), nil
	}
	return nil, errors.New("unexpected package verb")
}

func newLemonadeUpdate(t *testing.T) (*updateFixture, *fakeLemonade) {
	t.Helper()
	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	l := &fakeLemonade{t: t, selected: f.v1.Version, backend: f.v2.TestedBackendSHA256, model: f.v2.TestedModelRevision}
	l.install(t)
	withLemonadeJournal(t, f.journal)
	return f, l
}

// A Lemonade pin change passes pending to installed through the provider's
// verbs: the health check proves the staged root, activation moves the
// binding and records installed only on the provider's success object, and
// the retained version stays the active one through every earlier phase.
// Removal of the retired version asks the provider first.
func TestLemonadeUpdateMovesTheProviderBindingThroughItsVerbs(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	r := f.runner()
	f.advance(r, Requested)
	f.advance(r, Verified)
	f.advance(r, Staged)
	if f.active() != f.v1 || len(l.calls) != 0 {
		t.Fatalf("staging changed the active version or called the provider: %q", l.calls)
	}
	f.advance(r, Checked)
	checks := l.verb("check")
	if len(checks) != 1 || flagValue(checks[0], "--root") != f.root2 || flagValue(checks[0], "--version") != f.v2.Version || !slices.Contains(checks[0], "--json") {
		t.Fatalf("the health check ran as %q", checks)
	}
	if f.active() != f.v1 || l.selected != f.v1.Version || len(l.verb("activate")) != 0 {
		t.Fatal("a checked version became active")
	}
	l.activateReply = []byte(`{"version":1,"ok":false,"active_version":"2.0.0"}`)
	if _, err := r.Advance(f.ctx, f.component); err == nil || f.effect().Phase != Checked || f.active() != f.v1 {
		t.Fatalf("activation without the provider's success object recorded installed: %v %+v", err, f.effect())
	}
	l.activateReply = nil
	l.activateErr = &lemonadeExit{Code: 1, Reason: "engine files"}
	if _, err := r.Advance(f.ctx, f.component); err == nil || f.effect().Phase != Checked || f.active() != f.v1 {
		t.Fatalf("a failed activation recorded installed: %v %+v", err, f.effect())
	}
	l.activateErr = nil
	effect := f.advance(r, Installed)
	if f.active() != f.v2 || len(effect.Retired) != 1 || effect.Retired[0] != f.v1 || l.selected != f.v2.Version {
		t.Fatalf("activation did not move the binding and the journal together: %+v, provider at %s", effect, l.selected)
	}
	activations := l.verb("activate")
	if flagValue(activations[len(activations)-1], "--version") != f.v2.Version {
		t.Fatalf("activation ran as %q", activations)
	}
	effect = f.advance(r, Installed)
	retires := l.verb("retire")
	if len(effect.Retired) != 0 || len(retires) != 1 || flagValue(retires[0], "--version") != f.v1.Version {
		t.Fatalf("removal did not retire the earlier version through the provider: %+v %q", effect, retires)
	}
	if _, err := os.Lstat(f.root1); !os.IsNotExist(err) {
		t.Fatalf("the earlier version remains: %v", err)
	}
}

// A crash after package activate and before the journal transition: the next
// Advance activates once more, the provider finds itself bound already and
// copies nothing, and the journal records installed.
func TestLemonadeActivationRerunsAfterACrash(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	for _, phase := range []Phase{Requested, Verified, Staged, Checked} {
		f.advance(f.runner(), phase)
	}
	crashing := f.runner()
	crashing.crashAfter = crashAt("lemonade-activate")
	if _, err := crashing.Advance(f.ctx, f.component); !errors.Is(err, errCrash) {
		t.Fatalf("crash after activation not simulated: %v", err)
	}
	if f.effect().Phase != Checked || f.active() != f.v1 || l.selected != f.v2.Version {
		t.Fatalf("crash point not reached: %+v", f.effect())
	}
	f.advance(f.runner(), Installed)
	if len(l.verb("activate")) != 2 || l.copies != 1 || f.active() != f.v2 {
		t.Fatalf("the rerun made %d activations and %d copies", len(l.verb("activate")), l.copies)
	}
}

// A refused health check retires the staged version without any activation:
// a pairing that differs from the catalog's, and a tree the provider refuses.
func TestFailedLemonadeCheckRetiresTheStagedVersion(t *testing.T) {
	for name, refuse := range map[string]func(*fakeLemonade){
		"another backend": func(l *fakeLemonade) { l.backend = strings.Repeat("2", 64) },
		"another model":   func(l *fakeLemonade) { l.model = strings.Repeat("1", 40) },
		"refused tree":    func(l *fakeLemonade) { l.checkCode = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			f, l := newLemonadeUpdate(t)
			refuse(l)
			r := f.runner()
			for _, phase := range []Phase{Requested, Verified, Staged} {
				f.advance(r, phase)
			}
			if _, err := r.Advance(f.ctx, f.component); err == nil {
				t.Fatal("a refused health check passed")
			}
			effect := f.effect()
			for _, call := range l.verb("activate") {
				if flagValue(call, "--version") != f.v1.Version {
					t.Fatalf("a refused check activated the staged version: %q", call)
				}
			}
			if effect.Phase != Failed || f.active() != f.v1 || l.selected != f.v1.Version {
				t.Fatalf("a refused check activated or changed the active version: %+v %q", effect, l.calls)
			}
			if _, err := os.Lstat(f.root2); !os.IsNotExist(err) {
				t.Fatalf("the refused version remains: %v", err)
			}
		})
	}
	f, l := newLemonadeUpdate(t)
	l.checkCode = 1
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged} {
		f.advance(r, phase)
	}
	if _, err := r.Advance(f.ctx, f.component); err == nil || f.effect().Phase != Staged {
		t.Fatalf("a provider that did not answer retired the staged version: %v %+v", err, f.effect())
	}
}

// The bridge accepts the provider's binding to the retained version while an
// update is staged and refuses one to the staged version; after activation
// the reverse. reviewedLemonadeArchive and ConfigureLemonade keep working on
// the retained version during the update.
func TestLemonadeBridgeFollowsTheJournalsActiveVersion(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cache, "OpenAbstractions", "providers", "lemonade")
	bound := map[string]lemonadeInfo{}
	for _, a := range []Artifact{f.v1, f.v2} {
		info := lemonadeInfoFor(t, a, root, true)
		info.KnownPackages = []lemonadeKnownPackage{lemonadeInfoFor(t, f.v1, root, false).KnownPackages[0], lemonadeInfoFor(t, f.v2, root, false).KnownPackages[0]}
		bound[a.Version] = info
	}
	reply := bound[f.v1.Version]
	verified := true
	provider := l.run
	lemonadeCLI = func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "setup-info":
			return json.Marshal(reply)
		case "verify-setup":
			return json.Marshal(lemonadeProof{Version: 1, Verified: verified})
		}
		return provider(ctx, args...)
	}
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged} {
		f.advance(r, phase)
	}
	if _, err := readLemonadeInfo(f.ctx); err != nil {
		t.Fatalf("the retained version's binding was refused during the update: %v", err)
	}
	reply = bound[f.v2.Version]
	if _, err := readLemonadeInfo(f.ctx); err == nil {
		t.Fatal("a binding to the staged version was accepted before activation")
	}
	reply = bound[f.v1.Version]
	if err := reviewedLemonadeArchive(f.v1); err != nil {
		t.Fatalf("the retained version's receipt: %v", err)
	}
	inputs := t.TempDir()
	var paths []string
	for _, name := range []string{"main", "encoder", "vae", "backend.zip"} {
		path := filepath.Join(inputs, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := ConfigureLemonade(f.ctx, root, paths[0], paths[1], paths[2], paths[3], nil, false); err != nil {
		t.Fatalf("ConfigureLemonade during a staged update: %v", err)
	}
	f.advance(r, Checked)
	f.advance(r, Installed)
	if _, err := readLemonadeInfo(f.ctx); err == nil {
		t.Fatal("a binding to the retired version was accepted after activation")
	}
	reply = bound[f.v2.Version]
	if _, err := readLemonadeInfo(f.ctx); err != nil {
		t.Fatalf("the activated version's binding was refused: %v", err)
	}
}

// A definitive activation refusal (exit 2, such as a private model of another
// revision) fails the update as a refused health check does: the staged
// version is retired and removed, and the retained one stays active and
// selected.
func TestRefusedLemonadeActivationRetiresTheStagedVersion(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged, Checked} {
		f.advance(r, phase)
	}
	l.activateErr, l.activateRefuses = &lemonadeExit{Code: 2, Reason: "model revision differs; --replace-private-model"}, f.v2.Version
	if _, err := r.Advance(f.ctx, f.component); err == nil {
		t.Fatal("a refused activation passed")
	}
	effect := f.effect()
	if effect.Phase != Failed || f.active() != f.v1 || l.selected != f.v1.Version || len(effect.Retired) != 0 {
		t.Fatalf("a refused activation did not fail and retire the staged version: %+v, provider at %s", effect, l.selected)
	}
	if _, err := os.Lstat(f.root2); !os.IsNotExist(err) {
		t.Fatalf("the refused version remains: %v", err)
	}
	if !f.installed(f.v1, f.root1) {
		t.Fatal("the retained version was touched")
	}
}

// ReconcilePrior of an earlier plan's update interrupted after package
// activate: the provider selects the new version while the journal keeps the
// retained one. Removal of the never-activated version first selects the
// retained version again, then retires the new one.
func TestReconcilePriorRebindsTheRetainedLemonadeVersion(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged, Checked} {
		f.advance(r, phase)
	}
	l.selected = f.v2.Version
	third := nextRelease(*f.journal.catalogOverride, f.component, "3.0.0", f.v1, f.v2)
	f.journal.catalogOverride = &third
	l.calls = nil
	status, err := NewRunner(f.journal, nil).ReconcilePrior(f.ctx, f.component)
	if err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, call := range l.calls {
		verbs = append(verbs, call[1]+" "+flagValue(call, "--version"))
	}
	if want := []string{"activate " + f.v1.Version, "retire " + f.v2.Version}; !slices.Equal(verbs, want) {
		t.Fatalf("the provider saw %q, want %q", verbs, want)
	}
	effect := status.Components[0].Effect
	if effect.Phase != Failed || len(effect.Retired) != 0 || l.selected != f.v1.Version {
		t.Fatalf("the never-activated version was not retired behind the retained one: %+v, provider at %s", effect, l.selected)
	}
	if _, err := os.Lstat(f.root2); !os.IsNotExist(err) {
		t.Fatalf("the never-activated version remains: %v", err)
	}
}

// When the retained version can no longer be selected (its model revision
// was replaced), removal stops with a message naming the remedy; the folder
// and the retired entry stay.
func TestUnselectableRetainedLemonadeVersionStopsRemoval(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged, Checked} {
		f.advance(r, phase)
	}
	l.selected = f.v2.Version
	third := nextRelease(*f.journal.catalogOverride, f.component, "3.0.0", f.v1, f.v2)
	f.journal.catalogOverride = &third
	l.activateErr = &lemonadeExit{Code: 2, Reason: "model revision differs"}
	_, err := NewRunner(f.journal, nil).ReconcilePrior(f.ctx, f.component)
	if err == nil || !strings.Contains(err.Error(), "cannot select "+f.v1.Version) || len(l.verb("retire")) != 0 {
		t.Fatalf("removal went on or names no remedy: %v %q", err, l.calls)
	}
	if !f.installed(f.v2, f.root2) || len(f.effect().Retired) != 1 {
		t.Fatalf("the selected version's folder or its retired entry is gone: %+v", f.effect())
	}
}

// Removal of a retired Lemonade version waits while the provider reports it
// held: the folder and the retired entry stay, and the next Advance retries.
func TestHeldLemonadeVersionWaitsForRemoval(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged, Checked, Installed} {
		f.advance(r, phase)
	}
	l.retireCode = 3
	_, err := r.Advance(f.ctx, f.component)
	if err == nil || !strings.Contains(err.Error(), "in use") || !f.installed(f.v1, f.root1) || len(f.effect().Retired) != 1 {
		t.Fatalf("a held version was removed or the refusal names no retry: %v %+v", err, f.effect())
	}
	l.retireCode = 0
	if effect := f.advance(r, Installed); len(effect.Retired) != 0 || len(l.verb("retire")) != 2 {
		t.Fatalf("the retry did not remove the version: %+v", effect)
	}
	if _, err := os.Lstat(f.root1); !os.IsNotExist(err) {
		t.Fatalf("the earlier version remains: %v", err)
	}
}

// checkChange admits a Lemonade pin change into a new folder with the same
// install handler and refuses one that reuses the installed folder.
func TestLemonadePinChangeFollowsThePackageRules(t *testing.T) {
	f, _ := newLemonadeUpdate(t)
	r := f.runner()
	if err := r.checkChange("lemonade-engine", f.v2, f.v1); err != nil {
		t.Fatalf("a Lemonade pin change into a new folder was refused: %v", err)
	}
	same := f.v2
	same.Version = f.v1.Version
	if err := r.checkChange("lemonade-engine", same, f.v1); err == nil {
		t.Fatal("a Lemonade pin change reusing the installed folder was accepted")
	}
	other := f.v2
	other.Action = "msi_per_machine"
	if err := r.checkChange("lemonade-engine", other, f.v1); err == nil {
		t.Fatal("a Lemonade pin change with other install rights was accepted")
	}
}
