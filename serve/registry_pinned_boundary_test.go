package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestRegistryCompat1687BoundaryRetirementFencesEveryEdit(t *testing.T) {
	for _, kind := range []string{"replace", "declare", "withdraw", "close"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, caller := registryObserverFixture(t)
				ctx := context.Background()
				d := extensionDeclaration()
				added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
				if added.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatal(added)
				}
				old := r.providers.lookup(d.Name)
				old.mu.Lock()
				old.started = true
				old.mu.Unlock()
				joined := make(chan wire.DeclarationChange, 1)
				defer func() { close(old.done); <-joined }()
				d.Models = []string{"first"}
				go func() { joined <- r.ReplacePinned(ctx, caller, added.Revision, wire.PinnedDeclaration{Declaration: d}) }()
				<-old.stop // Actual existing retirement owner is now waiting on done.
				current := r.DeclarationsPinned(ctx, caller)
				d.Models = []string{"second"}
				if kind == "close" {
					next := r.providers.lookup(d.Name)
					closed := make(chan struct{})
					go func() { _ = r.providers.Close(); close(closed) }()
					<-next.stop
					closedAgain := make(chan struct{})
					go func() { _ = r.providers.Close(); close(closedAgain) }()
					synctest.Wait() // Only owned channel waits remain.
					select {
					case <-closed:
						t.Fatal("Close skipped held original retirement")
					default:
					}
					select {
					case <-closedAgain:
						t.Fatal("concurrent Close skipped original retirement")
					default:
					}
					return
				}
				var got wire.DeclarationChange
				switch kind {
				case "replace":
					got = r.ReplacePinned(ctx, caller, current.Revision, wire.PinnedDeclaration{Declaration: d})
				case "declare":
					got = r.DeclarePinned(ctx, caller, current.Revision, wire.PinnedDeclaration{Declaration: d})
				case "withdraw":
					got = r.Withdraw(ctx, caller, current.Revision, d.Name)
				}
				if got.Outcome != wire.DeclarationEditOutcomeConflict || got.Reason != "retiring" {
					t.Fatal("edit crossed original retirement", got)
				}
				if after := r.DeclarationsPinned(ctx, caller); after.Revision != current.Revision {
					t.Fatal("retirement fence changed declaration")
				}
			})
		})
	}
}

func TestRegistryCompat1687BoundaryConditionalAddAndRemove(t *testing.T) {
	for _, kind := range []string{"add", "empty-add", "remove"} {
		t.Run(kind, func(t *testing.T) {
			r, caller := registryObserverFixture(t)
			ctx := context.Background()
			d := extensionDeclaration()
			initial := r.DeclarationsPinned(ctx, caller)
			file := providerFile{Version: providerFileVersion, Declaration: declarationOf(d), DeclaredBy: caller.Program}
			expected := initial.Revision
			if kind == "remove" {
				added := r.providers.add(expected, file)
				if added.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatal(added)
				}
				expected = added.Revision
				file = r.providers.lookup(d.Name).file
			}
			competing := file
			competing.Generation = "competing"
			competing.Declaration.Models = []string{"competing"}
			var actual []byte
			mutate := func(path string, base []byte, value any) error {
				if kind == "empty-add" {
					f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						return err
					}
					if err := f.Close(); err != nil {
						return err
					}
				} else if err := writeAtomically(path, competing); err != nil {
					return err
				}
				var err error
				actual, err = os.ReadFile(path)
				if err != nil {
					return err
				}
				return providerMutator(ctx, nil)(path, base, value)
			}
			var got wire.DeclarationChange
			if kind == "remove" {
				got = r.providers.removeWith(expected, d.Name, mutate)
			} else {
				got = r.providers.addWith(expected, file, mutate)
			}
			if got.Outcome != wire.DeclarationEditOutcomeConflict {
				t.Fatal("competing target not refused", got)
			}
			after, err := os.ReadFile(filepath.Join(r.providers.dir, d.Name+".json"))
			if err != nil || !bytes.Equal(actual, after) {
				t.Fatal("competing target overwritten/deleted", err)
			}
		})
	}
}

func TestRegistryCompat1687BoundaryDisabledBytesAndUnreadableState(t *testing.T) {
	t.Run("competing-disabled", func(t *testing.T) {
		r, _ := registryObserverFixture(t)
		path := r.providers.disabledPath
		if err := writeAtomically(path, []string{"kept"}); err != nil {
			t.Fatal(err)
		}
		_, base, err := r.providers.disabledDeclarations()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeAtomically(path, []string{"kept", "competing"}); err != nil {
			t.Fatal(err)
		}
		prior, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.providers.setDisabledObserved(context.Background(), "mine", true, base, nil); !errors.Is(err, cas.ErrMoved) && !errors.Is(err, cas.ErrTooLarge) {
			t.Fatal("disabled snapshot was not bound", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(prior, after) {
			t.Fatal("lost concurrent disabled name", err)
		}
	})
	for _, kind := range []string{"invalid-disabled", "directory-disabled", "installation-io"} {
		t.Run(kind, func(t *testing.T) {
			r, caller := registryObserverFixture(t)
			ctx := context.Background()
			d := extensionDeclaration()
			added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
			if added.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatal(added)
			}
			path := filepath.Join(r.providers.dir, d.Name+".json")
			prior, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			old := r.providers.lookup(d.Name)
			switch kind {
			case "invalid-disabled":
				if err := os.WriteFile(r.providers.disabledPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory-disabled":
				if err := os.Mkdir(r.providers.disabledPath, 0700); err != nil {
					t.Fatal(err)
				}
			case "installation-io":
				r.providers.installation = filepath.Join(t.TempDir(), "not-directory")
				if err := os.WriteFile(r.providers.installation, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			d.Models = []string{"replacement"}
			for _, got := range []wire.DeclarationChange{r.ReplacePinned(ctx, caller, added.Revision, wire.PinnedDeclaration{Declaration: d}), r.Withdraw(ctx, caller, added.Revision, d.Name)} {
				if got.Outcome != wire.DeclarationEditOutcomeUnavailable {
					t.Fatal("unreadable state became empty", got)
				}
			}
			d.Name = "new"
			if got := r.DeclarePinned(ctx, caller, added.Revision, wire.PinnedDeclaration{Declaration: d}); got.Outcome != wire.DeclarationEditOutcomeUnavailable {
				t.Fatal(got)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(prior, after) || r.providers.lookup(old.file.Declaration.Name) != old {
				t.Fatal("failed admission changed prior", err)
			}
			select {
			case <-old.stop:
				t.Fatal("precommit refusal retired old lifetime")
			default:
			}
		})
	}
}

func TestRegistryCompat1687BoundaryAuthorityAfterNativeCASWait(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d := extensionDeclaration()
	added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	path := filepath.Join(r.providers.dir, d.Name+".json")
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := cas.LockContext(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	unlockOnce := sync.OnceFunc(unlock)
	defer unlockOnce()
	var count atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	r.operator.credentials.policy.Clock = func() time.Time {
		if count.Add(1) == 2 {
			close(entered)
			<-release
		}
		return time.Now()
	}
	result := make(chan wire.DeclarationChange, 1)
	go func() { result <- r.ReplacePinned(ctx, caller, added.Revision, wire.PinnedDeclaration{Declaration: d}) }()
	<-entered // Current post-p.mu admission has already read its permitted snapshot.
	if err := r.operator.credentials.policy.Revoke(rwire.Subject{Account: caller.Account, Program: caller.Program}, ActionProviderManage, credentials.ResourceAccount); err != nil {
		t.Fatal(err)
	}
	releaseOnce()
	unlockOnce()
	if got := <-result; got.Outcome != wire.DeclarationEditOutcomeForbidden {
		t.Fatal("CAS wait reused stale authority", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(prior, after) {
		t.Fatal("revoked CAS admission changed target", err)
	}
}

func TestRegistryCompat1687BoundaryContextAndAuthorityAfterWait(t *testing.T) {
	for _, kind := range []string{"v1add", "v2add", "pinnedadd", "replace", "withdraw", "hostadd", "hostremove"} {
		for _, end := range []string{"cancel", "revoke"} {
			t.Run(kind+"/"+end, func(t *testing.T) {
				r, caller := registryObserverFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				action := ActionProviderManage
				if kind == "hostadd" || kind == "hostremove" {
					action = inference.ActionHostManage
					if err := r.operator.credentials.runtimeRights.install([]string{action}, []installationRule{{action, credentials.ResourceAccount}}); err != nil {
						t.Fatal(err)
					}
					ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "spend.json"), nil, nil)
					if err != nil {
						t.Fatal(err)
					}
					r.operator.ceilings = ceilings
					r.operator.router = router.New()
					r.operator.report = func(err error) { t.Log(err) }
				}
				d := extensionDeclaration()
				host := iwire.HostEntry{Name: d.Name, Hosted: true, Kind: "fixture/test@1", Base: "https://fixture.invalid", Profiles: []string{"chat"}}
				if kind == "hostremove" {
					d = hostDeclaration(host).wire()
				}
				expected := r.DeclarationsPinned(context.Background(), caller).Revision
				if kind == "replace" || kind == "withdraw" || kind == "hostremove" {
					added := r.DeclarePinned(context.Background(), caller, expected, wire.PinnedDeclaration{Declaration: d})
					if added.Outcome != wire.DeclarationEditOutcomeApplied {
						t.Fatal(added)
					}
					expected = added.Revision
				}
				path := filepath.Join(r.providers.dir, d.Name+".json")
				prior, priorErr := os.ReadFile(path)
				var first atomic.Bool
				entered, release := make(chan struct{}), make(chan struct{})
				r.operator.credentials.policy.Clock = func() time.Time {
					if first.CompareAndSwap(false, true) {
						close(entered)
						<-release
					}
					return time.Now()
				}
				r.providers.mu.Lock()
				result := make(chan wire.DeclarationChange, 1)
				go func() {
					var got wire.DeclarationChange
					switch kind {
					case "v1add":
						got = r.Declare(ctx, caller, expected, d)
					case "v2add":
						got = r.DeclareV2(ctx, caller, expected, wire.DeclarationWithExtensions{Declaration: d})
					case "pinnedadd":
						got = r.DeclarePinned(ctx, caller, expected, wire.PinnedDeclaration{Declaration: d})
					case "replace":
						got = r.ReplacePinned(ctx, caller, expected, wire.PinnedDeclaration{Declaration: d})
					case "withdraw":
						got = r.Withdraw(ctx, caller, expected, d.Name)
					case "hostadd":
						v := r.operator.AddHost(ctx, caller, expected, host)
						got.Outcome = registryCompat1687HostOutcome(v.Outcome)
					case "hostremove":
						v := r.operator.RemoveHost(ctx, caller, expected, d.Name)
						got.Outcome = registryCompat1687HostOutcome(v.Outcome)
					}
					result <- got
				}()
				<-entered // Existing policy clock runs AFTER its initial file snapshot.
				if end == "cancel" {
					cancel()
				} else if err := r.operator.credentials.policy.Revoke(rwire.Subject{Account: caller.Account, Program: caller.Program}, action, credentials.ResourceAccount); err != nil {
					t.Fatal(err)
				}
				close(release)
				r.providers.mu.Unlock()
				got := <-result
				want := wire.DeclarationEditOutcomeForbidden
				if end == "cancel" {
					want = wire.DeclarationEditOutcomeUnavailable
				}
				if got.Outcome != want {
					t.Fatal("expired admission did not retain its refusal", got)
				}
				after, err := os.ReadFile(path)
				if !bytes.Equal(prior, after) || (os.IsNotExist(priorErr) != os.IsNotExist(err)) {
					t.Fatal("expired admission changed target", err)
				}
			})
		}
	}
}

func registryCompat1687HostOutcome(outcome iwire.EditOutcome) wire.DeclarationEditOutcome {
	for _, candidate := range wire.DeclarationEditOutcomeValues() {
		if hostEditOf(candidate) == outcome {
			return candidate
		}
	}
	return 0
}

func TestRegistryCompat1687BoundaryCompetingGenerationAtCAS(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d := extensionDeclaration()
	added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	next := r.providers.lookup(d.Name).file
	competing := next
	competing.Generation = "competing"
	competing.Declaration.Models = []string{"other"}
	next.Declaration.Models = []string{"mine"}
	got := r.providers.replaceWith(added.Revision, next, func(path string, base []byte, value any) error {
		if err := writeAtomically(path, competing); err != nil {
			return err
		}
		return providerMutator(ctx, nil)(path, base, value)
	})
	if got.Outcome != wire.DeclarationEditOutcomeConflict {
		t.Fatal("competing generation overwritten", got)
	}
	page := r.DeclarationsPinned(ctx, caller)
	if len(page.Declarations) != 1 || !bytes.Equal([]byte(page.Declarations[0].State.Declaration.Models[0]), []byte("other")) {
		t.Fatal(page)
	}
}
