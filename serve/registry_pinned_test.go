package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

type registryCompat1687Projection struct {
	wire.RegistryV2
	page wire.DeclarationListV2
}

func (p registryCompat1687Projection) Declarations() (wire.DeclarationListV2, error) {
	return p.page, nil
}

type registryCompat1687Exchange func([]byte) ([]byte, error)

func (f registryCompat1687Exchange) ExchangeFrame(frame []byte) ([]byte, error) { return f(frame) }

func TestRegistryCompat1687PublishedProjectionWire(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d, trust := registryRemoteFixture(t, "pinned")
	added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d, RemoteServerKeySHA256: trust.ServerKeySHA256})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	dispatch := wire.RegistryV2Dispatcher{Handler: registryCompat1687Projection{page: r.DeclarationsV2(ctx, caller)}}
	var response []byte
	client := wire.NewRegistryV2Client(registryCompat1687Exchange(func(frame []byte) ([]byte, error) {
		var err error
		response, err = dispatch.ExchangeFrame(frame)
		return response, err
	}))
	page, err := client.Declarations()
	if err != nil || len(page.Declarations) != 1 {
		t.Fatal(page, err)
	}
	if bytes.Contains(response, []byte("remote_server_key_sha256")) {
		t.Fatal("published projection leaked pin field")
	}
	if target := os.Getenv("OA_REGISTRYCOMPAT1687_OLD_RESPONSE"); target != "" {
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(response)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal(writeErr, closeErr)
		}
	}
}

type registryCompat1687Unsupported struct {
	wire.RegistryV2
	calls int
}

func (p *registryCompat1687Unsupported) DeclarePinned(string, wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	p.calls++
	return wire.DeclarationChange{}, wire.DispatchError("unknown_method")
}
func (p *registryCompat1687Unsupported) ReplacePinned(string, wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	p.calls++
	return wire.DeclarationChange{}, wire.DispatchError("unknown_method")
}
func TestRegistryCompat1687UnsupportedMethodHasNoEditFallback(t *testing.T) {
	handler := &registryCompat1687Unsupported{}
	dispatch := &wire.RegistryV2Dispatcher{Handler: handler}
	client := wire.NewRegistryV2Client(dispatch)
	for _, replace := range []bool{false, true} {
		var err error
		if replace {
			_, err = client.ReplacePinned("rev", wire.PinnedDeclaration{Declaration: extensionDeclaration()})
		} else {
			_, err = client.DeclarePinned("rev", wire.PinnedDeclaration{Declaration: extensionDeclaration()})
		}
		var service *wire.ServiceError
		if !errors.As(err, &service) || service.Code != wire.ServiceErrorCodeUnknownMethod {
			t.Fatal(err)
		}
	}
	if handler.calls != 2 {
		t.Fatal("unsupported edit retried", handler.calls)
	}
}

func TestRegistryCompat1687PinlessV2RefusesBeforeEffects(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d, _ := registryRemoteFixture(t, "pinless")
	before := r.DeclarationsV2(ctx, caller)
	got := r.DeclareV2(ctx, caller, before.Revision, wire.DeclarationWithExtensions{Declaration: d})
	if got.Outcome != wire.DeclarationEditOutcomeInvalid || got.Reason != "remote:registry@2" {
		t.Fatal(got)
	}
	after := r.DeclarationsV2(ctx, caller)
	if before.Revision != after.Revision || len(after.Declarations) != 0 {
		t.Fatal(after)
	}
}

func TestRegistryCompat1687ReplaceAtomicGeneration(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d := extensionDeclaration()
	first := r.DeclarationsPinned(ctx, caller)
	added := r.DeclarePinned(ctx, caller, first.Revision, wire.PinnedDeclaration{Declaration: d})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	old := r.providers.lookup(d.Name)
	old.mu.Lock()
	old.readiness = wire.DeclarationReadinessReady
	old.mu.Unlock()
	oldID := old.file.bindingID()
	oldRows, _, err := r.providers.listPinned()
	if err != nil {
		t.Fatal(err)
	}
	d.Models = []string{"replacement"}
	seen := 0
	r.providers.Watch(func() {
		seen++
		page := r.DeclarationsPinned(ctx, caller)
		if len(page.Declarations) != 1 || !slices.Equal(page.Declarations[0].State.Declaration.Models, d.Models) {
			t.Errorf("missing/mixed replacement: %+v", page)
		}
	})
	changed := r.ReplacePinned(ctx, caller, added.Revision, wire.PinnedDeclaration{Declaration: d})
	if changed.Outcome != wire.DeclarationEditOutcomeApplied || changed.Revision == added.Revision || seen != 1 {
		t.Fatal(changed, seen)
	}
	next := r.providers.lookup(d.Name)
	if next == old || next.file.bindingID() == oldID || next.state().Readiness == wire.DeclarationReadinessReady {
		t.Fatal("old readiness/generation retained")
	}
	select {
	case <-old.stop:
	default:
		t.Fatal("old supervisor not retired")
	}
	if !slices.Equal(oldRows[0].State.Declaration.Models, []string{"image-model"}) {
		t.Fatal("snapshot mutated")
	}
	if hosts := r.providers.nativeInferenceHosts(); len(hosts) != 1 || hosts[0].BindingID == oldID {
		t.Fatal("old native route retained", hosts)
	}
	old.start()
	old.mu.Lock()
	started := old.started
	old.mu.Unlock()
	if started {
		t.Fatal("retired supervisor restarted")
	}
}

func TestRegistryCompat1687ReplacePrecommitRefusals(t *testing.T) {
	for _, kind := range []string{"stale", "endpoint", "invalid", "paired", "installed", "product", "disabled", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			r, caller := registryObserverFixture(t)
			ctx := context.Background()
			d := extensionDeclaration()
			initial := r.DeclarationsPinned(ctx, caller)
			added := r.DeclarePinned(ctx, caller, initial.Revision, wire.PinnedDeclaration{Declaration: d})
			if added.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatal(added)
			}
			path := filepath.Join(r.providers.dir, d.Name+".json")
			prior, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			expected := added.Revision
			want := wire.DeclarationEditOutcomeConflict
			switch kind {
			case "stale":
				expected = initial.Revision
			case "endpoint":
				other := d
				other.Name = "other"
				other.Endpoint = "other"
				edit := r.DeclarePinned(ctx, caller, expected, wire.PinnedDeclaration{Declaration: other})
				if edit.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatal(edit)
				}
				expected = edit.Revision
				d.Endpoint = other.Endpoint
			case "invalid":
				d.Models = []string{""}
				want = wire.DeclarationEditOutcomeInvalid
			case "paired":
				r.providers.pairedNameAdmission = func(string) error { return errPairingConflict }
			case "installed":
				r.providers.installation = t.TempDir()
				if err := os.WriteFile(filepath.Join(r.providers.installation, d.Name+".json"), prior, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				want = wire.DeclarationEditOutcomeForbidden
			case "product":
				f := r.providers.lookup(d.Name).file
				f.Source = declarationSourceProduct
				r.providers.products = func() []providerFile { return []providerFile{f} }
				r.providers.productHas = false
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				want = wire.DeclarationEditOutcomeForbidden
			case "disabled":
				r.providers.installation = t.TempDir()
				if err := os.WriteFile(filepath.Join(r.providers.installation, d.Name+".json"), prior, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := r.providers.disable(d.Name); err != nil {
					t.Fatal(err)
				}
				want = wire.DeclarationEditOutcomeForbidden
			case "unknown":
				d.Name = "absent"
				want = wire.DeclarationEditOutcomeUnknown
			}
			if kind == "installed" || kind == "product" || kind == "disabled" {
				_, expected, err = r.providers.read()
				if err != nil {
					t.Fatal(err)
				}
			}
			before := r.DeclarationsPinned(ctx, caller)
			got := r.ReplacePinned(ctx, caller, expected, wire.PinnedDeclaration{Declaration: d})
			if got.Outcome != want {
				t.Fatal(kind, got)
			}
			after := r.DeclarationsPinned(ctx, caller)
			if before.Revision != after.Revision {
				t.Fatal("refusal edited revision")
			}
			if kind != "installed" && kind != "product" && kind != "disabled" {
				now, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(prior, now) {
					t.Fatal("refusal edited prior bytes", err)
				}
			}
		})
	}
}

func TestRegistryCompat1687WriteUncertaintyRereadsGeneration(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[committed], func(t *testing.T) {
			r, caller := registryObserverFixture(t)
			ctx := context.Background()
			d := extensionDeclaration()
			added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
			if added.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatal(added)
			}
			old := r.providers.lookup(d.Name)
			before := old.file.Generation
			next := old.file
			next.Declaration.Models = []string{"new"}
			got := r.providers.replaceWith(added.Revision, next, func(path string, base []byte, value any) error {
				if committed {
					if err := writeAtomically(path, value); err != nil {
						return err
					}
				}
				return errors.New("controlled write uncertainty")
			})
			if got.Outcome != wire.DeclarationEditOutcomeUnavailable {
				t.Fatal(got)
			}
			current := r.providers.lookup(d.Name)
			if (current.file.Generation != before) != committed || (current != old) != committed {
				t.Fatal("commit evidence not reconciled")
			}
			page := r.DeclarationsPinned(ctx, caller)
			if len(page.Declarations) != 1 {
				t.Fatal(page)
			}
			if committed && !slices.Equal(page.Declarations[0].State.Declaration.Models, []string{"new"}) {
				t.Fatal("new generation rolled back")
			}
		})
	}
}

func TestRegistryCompat1687UnknownWriteDoesNotAdvertiseOldReadiness(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d := extensionDeclaration()
	added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	old := r.providers.lookup(d.Name)
	got := r.providers.replaceWith(added.Revision, old.file, func(path string, _ []byte, _ any) error {
		if err := os.WriteFile(path, []byte("unreadable committed state"), 0600); err != nil {
			return err
		}
		return errors.New("controlled unknown commit")
	})
	if got.Outcome != wire.DeclarationEditOutcomeUnavailable || r.providers.lookup(d.Name) != nil {
		t.Fatal(got, "stale supervisor remained")
	}
	select {
	case <-old.stop:
	default:
		t.Fatal("old lifetime not retired")
	}
}

func TestRegistryCompat1687ReaderCannotSeeMissingReplacement(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d := extensionDeclaration()
	added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	next := r.providers.lookup(d.Name).file
	next.Declaration.Models = []string{"next"}
	entered, release := make(chan struct{}), make(chan struct{})
	changed := make(chan wire.DeclarationChange, 1)
	go func() {
		changed <- r.providers.replaceWith(added.Revision, next, func(path string, base []byte, value any) error {
			close(entered)
			<-release
			return providerMutator(ctx, nil)(path, base, value)
		})
	}()
	<-entered
	if r.providers.mu.TryLock() {
		r.providers.mu.Unlock()
		close(release)
		<-changed
		t.Fatal("commit released the reader lock")
	}
	observed := make(chan []wire.PinnedDeclarationState, 1)
	go func() {
		list, _, err := r.providers.listPinned()
		if err != nil {
			t.Error(err)
		}
		observed <- list
	}()
	close(release)
	if got := <-changed; got.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(got)
	}
	if page := <-observed; len(page) != 1 || !slices.Equal(page[0].State.Declaration.Models, []string{"next"}) {
		t.Fatal("missing/mixed replacement", page)
	}
}

func TestRegistryCompat1687PostcommitGrantFailureStaysApplied(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d := extensionDeclaration()
	added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	path := r.operator.credentials.path
	r.providers.Watch(func() {
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Rename(path+".saved", path); err != nil {
			t.Error(err)
		}
	})
	d.Resources = append(d.Resources, "card:1")
	got := r.ReplacePinned(ctx, caller, added.Revision, wire.PinnedDeclaration{Declaration: d})
	if got.Outcome != wire.DeclarationEditOutcomeApplied || !bytes.HasPrefix([]byte(got.Reason), []byte("rules:")) {
		t.Fatal(got)
	}
	current := r.providers.lookup(d.Name)
	if current == nil || !slices.Contains(current.file.Declaration.Resources, "card:1") {
		t.Fatal("committed declaration rolled back")
	}
}

func TestRegistryCompat1687AddAndRemoveUncertaintyRequiresReread(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "add", true: "remove"}[remove], func(t *testing.T) {
			r, caller := registryObserverFixture(t)
			ctx := context.Background()
			d := extensionDeclaration()
			expected := r.DeclarationsPinned(ctx, caller).Revision
			file := providerFile{Version: providerFileVersion, Declaration: declarationOf(d), DeclaredBy: caller.Program}
			if remove {
				added := r.providers.add(expected, file)
				if added.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatal(added)
				}
				expected = added.Revision
			}
			mutate := func(path string, base []byte, value any) error {
				if err := providerMutator(ctx, nil)(path, base, value); err != nil {
					return err
				}
				return errors.New("controlled postcommit uncertainty")
			}
			var got wire.DeclarationChange
			if remove {
				got = r.providers.removeWith(expected, d.Name, mutate)
			} else {
				got = r.providers.addWith(expected, file, mutate)
			}
			if got.Outcome != wire.DeclarationEditOutcomeUnavailable {
				t.Fatal(got)
			}
			page := r.DeclarationsPinned(ctx, caller)
			if page.Revision == expected {
				t.Fatal("committed mutation was rolled back")
			}
			if remove {
				if len(page.Declarations) != 0 || r.providers.lookup(d.Name) != nil {
					t.Fatal("removed generation remained", page)
				}
			} else {
				if len(page.Declarations) != 1 || page.Declarations[0].State.Readiness != wire.DeclarationReadinessUnreachable {
					t.Fatal("uncertain generation misreported", page)
				}
				if recovered := r.ReplacePinned(ctx, caller, page.Revision, wire.PinnedDeclaration{Declaration: d}); recovered.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatal(recovered)
				}
			}
		})
	}
}
