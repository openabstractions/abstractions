package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestNativeUserDirectorySeparationFailsClosed(t *testing.T) {
	root, separate := t.TempDir(), t.TempDir()
	if err := nativeUserDirectorySeparation(separate, root); err != nil {
		t.Fatalf("ordinary separate local directory: %v", err)
	}
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := nativeUserDirectorySeparation(inside, root); err == nil {
		t.Fatal("inside directory accepted as separate")
	}
	if runtime.GOOS == "windows" {
		refuse := identity.TreatDriveAsNetwork('Q')
		if err := nativeUserDirectorySeparation(`Q:\outside`, root); !errors.Is(err, identity.ErrNotLocalPath) {
			refuse()
			t.Fatalf("locality observation error was not preserved: %v", err)
		}
		refuse()
		stream := filepath.Join(root, "file:ads")
		if identity.LocalPath(stream) != nil || identity.Inside(stream, root) {
			t.Fatal("stream control no longer exposes false-as-permission")
		}
		if err := nativeUserDirectorySeparation(stream, root); !errors.Is(err, identity.ErrNotLocalPath) {
			t.Fatalf("a stream refusal was not preserved: %v", err)
		}
	}
}

func nativeArchiveFixture(t *testing.T, version ...string) (string, Artifact, []byte, nativeInstallPlan) {
	t.Helper()
	manifest := []byte("{\"fixture\":true}")
	backend := []byte("inert encoded backend fixture")
	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	doc := nativeCatalogFixture(t)
	row := nativeRow(doc)
	if len(version) != 0 {
		row["version"] = version[0]
	}
	row["variant"] = "oa-comfyui-native"
	row["action"] = "archive_per_user"
	row["install_identity"] = "oa-comfyui-native"
	row["application_program"] = "launch-helper.exe"
	row["service_program"] = "oa-backend/helper/python.exe"
	row["archive_tool_program"] = "archive-tool/7zr.exe"
	row["record_roots"] = []string{"oa-backend", "oa-control"}
	recordBytes := int64(2 * (121 + len("manifest.json") + 96 + len("helper/python.exe") + 96 + 121 + len("control.py") + 96))
	row["record_bytes"] = recordBytes
	resource := row["backend"].(map[string]any)
	resource["archive"] = map[string]any{"name": "backend.7z", "format": "7z", "bytes": len(backend), "sha256": hash(backend), "parts": []any{map[string]any{"name": "backend.7z", "bytes": len(backend), "sha256": hash(backend)}}}
	resource["provenance"].(map[string]any)["manifest"] = map[string]any{"name": "backend-manifest.json", "bytes": len(manifest), "sha256": hash(manifest)}
	entries := []archiveEntry{{name: "launch-helper.exe", data: "inert native image"}, {name: "archive-tool/7zr.exe", data: "inert decoder image"}, {name: "oa-control/control.py", data: "inert control"}}
	markBytes := int64(0)
	if len(version) != 0 {
		mark, err := json.Marshal(map[string]string{"fixture": "native", "version": version[0]})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, archiveEntry{name: "OA-PROVENANCE.json", data: string(mark)})
		markBytes = int64(len(mark))
	}
	source, pin := archiveFixture(t, entries...)
	row["binary_sha256"] = pin.SHA256
	row["binary_bytes"] = pin.Bytes
	files, dirs, err := expectedArchiveRoster(context.Background(), pin, source)
	if err != nil {
		t.Fatal(err)
	}
	pin.Version = row["version"].(string)
	pin.InstallIdentity = row["install_identity"].(string)
	markerBytes, err := nativeMarkerBase(pin, files, dirs)
	if err != nil {
		t.Fatal(err)
	}
	row["marker_bytes"] = markerBytes
	row["peak_bytes"] = pin.Bytes + int64(len(backend)+len("inert native image")+len("inert decoder image")+len("inert control")+len(manifest)+len("python")) + recordBytes + markerBytes + markBytes + 65536
	pins, err := parseAdopterAssets(nativeJSON(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	a := *pins.Apps[0].artifact(pins.ReleaseTag)
	plan := nativeInstallPlan{Directory: "oa-backend", Files: []archiveFileRecord{{Path: "manifest.json", Size: int64(len(manifest)), SHA256: hash(manifest)}, {Path: "helper/python.exe", Size: 6, SHA256: hash([]byte("python"))}}}
	plan.Manifest.Bytes = int64(len(manifest))
	plan.Manifest.SHA256 = hash(manifest)
	return source, a, backend, plan
}

func TestNativeArchiveRequiresWholeResourceAndPreservesPartialState(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "seal-refusal"}[refuse], func(t *testing.T) {
			if err := UseRuntimeState(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(UseUserState)
			source, a, backend, plan := nativeArchiveFixture(t)
			c := fixtureCatalog()
			for i := range c.Components {
				if c.Components[i].ID == fixtureComponent {
					c.Components[i].Artifact = &a
				}
			}
			j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
			ctx := context.Background()
			if _, err := j.SavePlan(ctx, mustPlanOf(t, j.Catalog(), fixtureApp, PyTorch)); err != nil {
				t.Fatal(err)
			}
			hostBytes, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			transfer := &fakeTransfer{state: "complete", bytes: hostBytes}
			runner := NewRunner(j, transfer)
			runner.nativeArchiveFreeBytes = func(string) (uint64, error) { return 1 << 62, nil }
			oldCommand, oldSeal, oldExtract := nativeArchiveCommand, nativeArchiveSealCommand, nativeArchiveExtract
			t.Cleanup(func() {
				nativeArchiveCommand, nativeArchiveSealCommand, nativeArchiveExtract = oldCommand, oldSeal, oldExtract
			})
			nativeArchiveSealCommand = func(_ context.Context, _ sevenZipPin, site string) ([]byte, error) {
				t.Fatalf("an unrelated archive received a shared runtime site: %s", site)
				return nil, nil
			}
			var operations []nativeArchiveOperation
			sealed, sealRefused, registered := false, refuse, 0
			user := t.TempDir()
			if err := privatedir.Tighten(user); err != nil {
				t.Fatal(err)
			}
			nativeArchiveCommand = func(_ context.Context, pin sevenZipPin, op nativeArchiveOperation) ([]byte, error) {
				operations = append(operations, op)
				if filepath.Base(pin.Path) != "launch-helper.exe" || pin.Bytes != int64(len("inert native image")) {
					t.Fatal("unbound host pin")
				}
				switch op {
				case nativeArchivePlan:
					return json.Marshal(plan)
				case nativeArchiveSeal:
					if sealRefused {
						return nil, errors.New("inert seal refusal")
					}
					sealed = true
				case nativeArchiveCheck:
					if !sealed {
						return nil, errors.New("backend not sealed")
					}
					return json.Marshal(map[string]string{"user_directory": user})
				case nativeArchiveRegister:
					if !sealed {
						t.Fatal("register-self ran before a sealed install")
					}
					registered++
					if registered == 1 {
						return nil, errors.New("inert first registration refusal")
					}
				}
				return nil, nil
			}
			nativeArchiveExtract = func(_ context.Context, in sevenZipArchive) error {
				if len(in.Parts) != 1 || in.ArchiveSHA256 != a.NativeResources.Backend.Archive.SHA256 || in.Tool.SHA256 == "" || filepath.Base(in.Tool.Path) != "7zr.exe" {
					t.Fatal("unpinned resource composition")
				}
				if err := verifyFile(in.Parts[0].Path, Artifact{Bytes: int64(len(backend)), SHA256: in.ArchiveSHA256}); err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Join(in.Destination, "helper"), 0700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(in.Destination, "manifest.json"), []byte("{\"fixture\":true}"), 0600); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(in.Destination, "helper/python.exe"), []byte("python"), 0600)
			}
			if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
				t.Fatal(err)
			} // host copied; next part recorded
			transfer.bytes = backend
			if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
				t.Fatal(err)
			}
			state, err := j.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if state.Effects[fixtureComponent].Phase != Verified || state.Effects[fixtureComponent].ResourceIndex != 1 || transfer.reserve != 2 {
				t.Fatalf("resource jobs: %+v", state.Effects)
			}
			_, err = runner.Advance(ctx, fixtureComponent)
			state, loadErr := j.Load(ctx)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if refuse {
				if err == nil || state.Effects[fixtureComponent].Phase != Uncertain {
					t.Fatalf("partial outcome: %+v %v", state.Effects, err)
				}
			} else if err != nil || state.Effects[fixtureComponent].Phase != Staged {
				t.Fatalf("complete outcome: %+v %v", state.Effects, err)
			}
			if !slices.Contains(operations, nativeArchivePlan) || !slices.Contains(operations, nativeArchiveSeal) {
				t.Fatal(operations)
			}
			root, err := installPath(a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "launch-helper.exe")); err != nil {
				t.Fatal("partial host silently removed")
			}
			if !refuse {
				archive := retainedNativeArchive{component: fixtureComponent, title: "Example App"}
				actual, err := nativeArchiveUserDirectory(ctx, a, root)
				if err != nil || actual != user {
					t.Fatalf("source-owned user directory: %q %v", actual, err)
				}
				// The install itself asks nothing; the workspace step runs
				// register-self, and a refused first request is retried.
				if registered != 0 {
					t.Fatalf("register-self ran %d times during the install", registered)
				}
				if err := archive.registerSelf(ctx, j); err == nil || registered != 1 {
					t.Fatalf("first registration: %d %v", registered, err)
				}
				if err := archive.registerSelf(ctx, j); err != nil || registered != 2 {
					t.Fatalf("second registration: %d %v", registered, err)
				}
				held, err := archive.holdProgram(ctx, j)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256([]byte("inert native image"))
				if held.Path != filepath.Join(root, "launch-helper.exe") || held.SHA256 != hex.EncodeToString(digest[:]) {
					_ = held.Close()
					t.Fatalf("held program differs from the reviewed host: %+v", held)
				}
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
				site := t.TempDir()
				reseals := 0
				nativeArchiveSealCommand = func(_ context.Context, pin sevenZipPin, actualSite string) ([]byte, error) {
					reseals++
					if pin.Path != held.Path || pin.SHA256 != held.SHA256 || actualSite != site {
						t.Fatalf("reseal command used the wrong host or runtime site: %+v, %q", pin, actualSite)
					}
					return nil, nil
				}
				if err := resealNativeArchiveRuntimeSite(ctx, a, site); err != nil || reseals != 1 {
					t.Fatalf("verified runtime reseal: %d %v", reseals, err)
				}
				control := filepath.Join(root, "oa-control", "control.py")
				if err := os.WriteFile(control, []byte("changed bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := resealNativeArchiveRuntimeSite(ctx, a, site); err == nil || reseals != 1 {
					t.Fatalf("changed host file reached runtime reseal: %d %v", reseals, err)
				}
				if err := archive.registerSelf(ctx, j); err == nil || registered != 2 {
					t.Fatalf("changed host file reached registration: %d %v", registered, err)
				}
				if held, err := archive.holdProgram(ctx, j); err == nil {
					_ = held.Close()
					t.Fatal("a changed host installation supplied a held program")
				}
			}
			if _, err := matchingReceipt(a, root); err == nil {
				t.Fatal("sealed resource removal bypassed its owner")
			}
			if !refuse {
				return
			}
			if registered != 0 {
				t.Fatal("a partial install registered")
			}
			// Check and recover redoes the partial install from the verified
			// downloads, without manual cleanup.
			sealRefused = false
			if _, err := runner.Retry(ctx, fixtureComponent); err != nil {
				t.Fatal(err)
			}
			if state, err := j.Load(ctx); err != nil || state.Effects[fixtureComponent].Phase != Verified {
				t.Fatalf("retry of the partial install: %+v %v", state.Effects, err)
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial folder kept: %v", err)
			}
			if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
				t.Fatal(err)
			}
			if state, err := j.Load(ctx); err != nil || state.Effects[fixtureComponent].Phase != Staged || registered != 0 {
				t.Fatalf("redone install: %+v registered %d %v", state.Effects, registered, err)
			}
		})
	}
}

func TestNativeArchiveDiskAdmissionKeepsPlatformDefaultAndMeasuredBounds(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	_, a, _, _ := nativeArchiveFixture(t)
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	peak, err := nativeArchivePeak(a, filepath.Join(stage, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	// A nil seam retains the exact production platform owner, including its
	// refusal on platforms where the Windows native offer is unavailable.
	defaultErr := nativeArchiveSpace(a, root, stage, nil)
	ownerErr := nativeArchiveSpace(a, root, stage, modelDiskFree)
	if (defaultErr == nil) != (ownerErr == nil) || defaultErr != nil && defaultErr.Error() != ownerErr.Error() {
		t.Fatalf("default differs from platform disk owner: %v / %v", defaultErr, ownerErr)
	}
	for _, failAt := range []int{0, 1, 2} {
		calls := 0
		err = nativeArchiveSpace(a, root, stage, func(path string) (uint64, error) {
			calls++
			want := root
			if calls == 2 {
				want = stage
			}
			if path != want {
				t.Fatalf("wrong measured path %q", path)
			}
			if calls == failAt {
				return uint64(peak) - 1, nil
			}
			return uint64(peak), nil
		})
		if (err != nil) != (failAt != 0) {
			t.Fatalf("space threshold %d: %v", failAt, err)
		}
		if failAt == 0 && calls != 2 {
			t.Fatal("both volumes not measured")
		}
	}
	measurementErr := errors.New("inert disk measurement refusal")
	err = nativeArchiveSpace(a, root, stage, func(string) (uint64, error) { return 0, measurementErr })
	if !errors.Is(err, measurementErr) {
		t.Fatal("disk error lost", err)
	}
}

func TestNativeArchiveRejectsPlanAndUnexpectedHostMembersBeforeExecution(t *testing.T) {
	source, a, _, plan := nativeArchiveFixture(t)
	for _, change := range []func(*nativeInstallPlan){func(p *nativeInstallPlan) { p.Directory = "foreign" }, func(p *nativeInstallPlan) { p.Manifest.SHA256 = "" }, func(p *nativeInstallPlan) { p.Files = append(p.Files, p.Files[0]) }} {
		copy := plan
		copy.Files = append([]archiveFileRecord{}, plan.Files...)
		change(&copy)
		raw, _ := json.Marshal(copy)
		if _, err := decodeNativeInstallPlan(raw, a); err == nil {
			t.Fatal("foreign plan accepted")
		}
	}
	root := filepath.Join(t.TempDir(), "installed")
	if err := InstallArchive(context.Background(), a, source, root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "foreign.dll"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	old := nativeArchiveCommand
	defer func() { nativeArchiveCommand = old }()
	called := false
	nativeArchiveCommand = func(context.Context, sevenZipPin, nativeArchiveOperation) ([]byte, error) {
		called = true
		return nil, nil
	}
	if err := installNativeResources(context.Background(), a, root, source, nil); err == nil || called {
		t.Fatalf("foreign host member reached code: called=%v err=%v", called, err)
	}
}
