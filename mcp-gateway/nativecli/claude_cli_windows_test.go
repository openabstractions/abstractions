package nativecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

func TestAgentSelection1784ActualNativeMetadata(t *testing.T) {
	if os.Getenv("OA_AGENT_SELECTION1784_DIAG") != "1" {
		t.Skip("explicit read-only native image receiving")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := currentClaudeImages(ctx)
	if err != nil {
		t.Logf("aggregate native metadata refused: %v", err)
	}
	defer func() {
		for _, row := range rows {
			if err := row.close(); err != nil {
				t.Error(err)
			}
		}
	}()
	t.Logf("admitted_native_CUI_images=%d product_family=%s", len(rows), claudeProductFamily)
	for _, row := range rows {
		if err := row.current(); err != nil {
			t.Fatal(err)
		}
		if err := row.image.Check(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("held_program=%s file_key=%s", row.image.Program(), row.image.FileKey())
	}
	if raw := os.Getenv("OA_AGENT_SELECTION1784_PID"); raw != "" {
		pid, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if openErr != nil {
			t.Logf("observed_pid=%d package_query_open_error=%v", pid, openErr)
			return
		}
		defer windows.CloseHandle(process)
		family, packageErr := claudeProcessPackage(process)
		t.Logf("observed_pid=%d package_family=%q native_no_package=%t error=%v", pid, family, errors.Is(packageErr, appModelNoPackage), packageErr)
		var path [32768]uint16
		length := uint32(len(path))
		if err := windows.QueryFullProcessImageName(process, 0, &path[0], &length); err != nil {
			t.Logf("observed image query refused=%v", err)
			return
		}
		program := windows.UTF16ToString(path[:length])
		image, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			t.Logf("observed image FileID refused=%v", err)
			return
		}
		defer image.Close()
		t.Logf("observed_image_program=%s readable_file_key=%s; file readability alone supplies no publisher admission", program, image.FileID().Key())
	}
}

func claudeCLIImageFixture(t *testing.T, path string) *Image {
	t.Helper()
	directory, err := programfiles.HoldDirectory(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	file, err := openArchiveSource(context.Background(), path)
	if err != nil {
		directory.Release()
		t.Fatal(err)
	}
	key, err := fileid.FileKeyOf(file)
	if err != nil {
		t.Fatal(err)
	}
	return &Image{file: file, directory: directory, program: path, key: key}
}

func TestClaudeCLISelectionDeduplicatesAndHoldsNativeLifetime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.exe")
	if err := os.WriteFile(path, []byte("inert executable fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	alive := [2]bool{true, true}
	closed := 0
	var rows []claudeProcessImage
	for i := range alive {
		index := i
		image := claudeCLIImageFixture(t, path)
		rows = append(rows, claudeProcessImage{image: image, current: func() error {
			if !alive[index] {
				return errors.New("ended")
			}
			return nil
		}, close: func() error { closed++; return image.Close() }})
	}
	probes := 0
	cli, err := selectClaudeImages(context.Background(), rows, func(ctx context.Context, program string) ([]byte, error) {
		probes++
		if program != path {
			t.Fatal(program)
		}
		return []byte("2.1.308 (Claude Code)\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 || cli.Version() != "2.1.308" || cli.FileKey() == "" {
		t.Fatal(probes, cli.Version())
	}
	alive[0] = false
	if err := cli.Check(context.Background()); err != nil {
		t.Fatal("another retained occurrence remains live", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err == nil {
		t.Fatal("selected image allowed replacement")
	}
	alive[1] = false
	if err := cli.Check(context.Background()); err == nil {
		t.Fatal("ended selection remained usable")
	}
	if err := cli.Close(); err != nil {
		t.Fatal(err)
	}
	if closed != 2 {
		t.Fatal("process holds not released", closed)
	}
	if err := cli.Check(context.Background()); err == nil {
		t.Fatal("closed selection remained usable")
	}
}

type claudeSavedImageFixture struct {
	nativeProductImage
	changed  bool
	closes   *int
	closeErr error
}

func (i *claudeSavedImageFixture) Check(ctx context.Context) error {
	if i.changed {
		return errors.New("current image or registered publisher changed")
	}
	return i.nativeProductImage.Check(ctx)
}
func (i *claudeSavedImageFixture) Close() error {
	*i.closes++
	return errors.Join(i.nativeProductImage.Close(), i.closeErr)
}

func claudeSavedLocator(t *testing.T, path string) string {
	t.Helper()
	id, err := fileid.Resolve(context.Background(), fileid.Self, path, fileid.Options{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := id.Stored()
	if err != nil {
		t.Fatal(err)
	}
	return stored.String()
}

func TestClaudeSavedIdentityHoldHasNoProcessOrVersionWork(t *testing.T) {
	program := filepath.Join(t.TempDir(), "never-executed.exe")
	if err := os.WriteFile(program, []byte("inert identity fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	locator := claudeSavedLocator(t, program)
	closed, holds := 0, 0
	var held *claudeSavedImageFixture
	cli, err := holdClaudeSavedCLI(t.Context(), []string{locator, locator}, func(_ context.Context, path string) (nativeProductImage, error) {
		holds++
		image := &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closed}
		if held == nil {
			held = image
		}
		return image, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cli.Version() != "" || len(cli.processes) != 0 || holds != 2 || closed != 1 || cli.Check(t.Context()) != nil {
		t.Fatal("pure identity hold performed command admission", holds, closed)
	}
	held.changed = true
	if cli.Check(t.Context()) == nil {
		t.Fatal("native image/anchor change ignored")
	}
	if err := cli.Close(); err != nil || closed != 2 {
		t.Fatal("native identity holds leaked", closed, err)
	}
	if _, err := holdClaudeSavedCLI(t.Context(), []string{"not-a-native-locator"}, func(context.Context, string) (nativeProductImage, error) {
		t.Fatal("invalid locator reached native hold")
		return nil, nil
	}); err == nil {
		t.Fatal("invalid locator accepted")
	}
}

func TestClaudeSavedCLIUsesFreshImageAfterVendorUpdateWithoutAProcess(t *testing.T) {
	program := filepath.Join(t.TempDir(), "code.exe")
	if err := os.WriteFile(program, []byte("old signed fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	locator := claudeSavedLocator(t, program)
	if err := os.Rename(program, program+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("new signed fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	closed, probes := 0, 0
	var held []*claudeSavedImageFixture
	cli, err := selectClaudeSavedCLI(context.Background(), []string{locator, locator}, func(_ context.Context, path string) ([]byte, error) {
		probes++
		if path != program {
			t.Fatal("saved FileID redirected admission from its configured location", path)
		}
		return []byte("2.1.309 (Claude Code)\n"), nil
	}, func(_ context.Context, path string) (nativeProductImage, error) {
		image := &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closed}
		held = append(held, image)
		return image, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if probes != 1 || len(held) != 2 || closed != 1 || len(cli.processes) != 0 || cli.Version() != "2.1.309" {
		t.Fatal("management admission did not use one fresh image and close its duplicate", probes, closed)
	}
	if err := cli.Check(context.Background()); err != nil {
		t.Fatal("management unexpectedly requires a Code session", err)
	}
	if err := os.Rename(program, program+".changed"); err == nil {
		t.Fatal("management image lost its rename fence")
	}
	held[0].changed = true
	if err := cli.Check(context.Background()); err == nil {
		t.Fatal("management ignored current image/anchor revocation")
	}
	if err := cli.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cli.Close(); err != nil || closed != 2 {
		t.Fatal("close was not idempotent", err, closed)
	}
	if err := cli.Check(context.Background()); err == nil {
		t.Fatal("closed management selection remained admitted")
	}
}

func TestClaudeSavedCLIRefusesMissingForeignAmbiguousAndChangedImages(t *testing.T) {
	for _, mode := range []string{"missing", "foreign", "ambiguous", "changed-before-probe", "changed-during-probe", "invalid-version", "oversized-version", "probe-error", "nil-hold", "malformed", "cancelled", "partial-hold-error", "duplicate-close-error"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			program := filepath.Join(root, "code.exe")
			if err := os.WriteFile(program, []byte("inert signed fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			stored := []string{claudeSavedLocator(t, program)}
			if mode == "missing" {
				if err := os.Remove(program); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "malformed" {
				stored = []string{"not a native locator"}
			}
			if mode == "duplicate-close-error" {
				stored = append(stored, stored[0])
			}
			if mode == "ambiguous" {
				other := filepath.Join(root, "other.exe")
				if err := os.WriteFile(other, []byte("another signed fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				stored = append(stored, claudeSavedLocator(t, other))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			closed, opened, probes := 0, 0, 0
			var image *claudeSavedImageFixture
			cli, err := selectClaudeSavedCLI(ctx, stored, func(context.Context, string) ([]byte, error) {
				probes++
				if mode == "changed-during-probe" {
					image.changed = true
				}
				switch mode {
				case "probe-error":
					return nil, errors.New("bounded command refused")
				case "invalid-version":
					return []byte("not Code"), nil
				case "oversized-version":
					return []byte(strings.Repeat("x", 257)), nil
				}
				return []byte("2.1.309 (Claude Code)\n"), nil
			}, func(_ context.Context, path string) (nativeProductImage, error) {
				if mode == "foreign" {
					return nil, errors.New("registered publisher mismatch")
				}
				if mode == "nil-hold" {
					return nil, nil
				}
				if _, err := os.Stat(path); err != nil {
					return nil, err
				}
				opened++
				image = &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closed, changed: mode == "changed-before-probe"}
				if mode == "duplicate-close-error" && opened == 2 {
					image.closeErr = errors.New("close completion unavailable")
				}
				if mode == "partial-hold-error" {
					return image, errors.New("native admission failed after hold")
				}
				return image, nil
			})
			if err == nil || cli != nil || closed != opened {
				t.Fatal("failed admission leaked a selection/hold", err, cli, opened, closed)
			}
			if (mode == "missing" || mode == "foreign" || mode == "ambiguous" || mode == "changed-before-probe" || mode == "nil-hold" || mode == "malformed" || mode == "cancelled" || mode == "partial-hold-error" || mode == "duplicate-close-error") && probes != 0 {
				t.Fatal("refused candidate reached version command", probes)
			}
		})
	}
}

func TestClaudeCLISelectionRefusesAmbiguousStaleAndInvalidVersions(t *testing.T) {
	for _, mode := range []string{"absent", "ambiguous", "ended", "changed-during-probe", "invalid", "oversized", "probe-error"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			var rows []claudeProcessImage
			closed, probes := 0, 0
			alive := true
			count := 1
			if mode == "absent" {
				count = 0
			}
			if mode == "ambiguous" {
				count = 2
			}
			for i := 0; i < count; i++ {
				path := filepath.Join(root, string(rune('a'+i))+".exe")
				if err := os.WriteFile(path, []byte("inert fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				image := claudeCLIImageFixture(t, path)
				rows = append(rows, claudeProcessImage{image: image, current: func() error {
					if !alive || mode == "ended" {
						return errors.New("ended")
					}
					return nil
				}, close: func() error { closed++; return image.Close() }})
			}
			_, err := selectClaudeImages(context.Background(), rows, func(context.Context, string) ([]byte, error) {
				probes++
				switch mode {
				case "changed-during-probe":
					alive = false
				case "invalid":
					return []byte("Desktop 3.6"), nil
				case "oversized":
					return []byte(strings.Repeat("x", 257)), nil
				case "probe-error":
					return nil, errors.New("probe refused")
				}
				return []byte("2.1.308 (Claude Code)"), nil
			})
			if err == nil || closed != count {
				t.Fatal(err, closed, count)
			}
			if (mode == "absent" || mode == "ambiguous" || mode == "ended") && probes != 0 {
				t.Fatal("probe ran before native admission", probes)
			}
		})
	}
}

func TestClaudeRegisteredActivationUsesOnlySingleCurrentApp(t *testing.T) {
	for _, row := range []struct {
		body  string
		valid bool
	}{{`<Package><Applications><Application Id="Claude"/></Applications></Package>`, true}, {`<Package><Applications/></Package>`, false}, {`<Package><Applications><Application Id="a"/><Application Id="b"/></Applications></Package>`, false}, {`<!DOCTYPE Package><Package/>`, false}, {`<Package><Applications><Application Id="../outside"/></Applications></Package>`, false}} {
		id, err := claudeManifestApplication(strings.NewReader(row.body))
		if (err == nil) != row.valid {
			t.Fatal(id, err, row.body)
		}
	}
	activated := 0
	selectApp := func(context.Context) (string, error) { return claudeProductFamily + "!Claude", nil }
	activate := func(app string) error {
		activated++
		if app != claudeProductFamily+"!Claude" {
			t.Fatal(app)
		}
		return nil
	}
	if err := openClaudeProduct(context.Background(), selectApp, activate); err != nil || activated != 1 {
		t.Fatal(err, activated)
	}
	for _, app := range []string{"claude.exe", "other!Claude", claudeProductFamily + "!../outside"} {
		if err := openClaudeProduct(context.Background(), func(context.Context) (string, error) { return app, nil }, activate); err == nil {
			t.Fatal("foreign activation accepted", app)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := openClaudeProduct(ctx, selectApp, activate); err == nil || activated != 1 {
		t.Fatal("cancelled activation ran", err, activated)
	}
}

func TestClaudeCLIUnknownCandidateRefusesBesideGoodImage(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		closed := 0
		rows, err := admitClaudeCandidates(context.Background(), []uint32{10, 11}, func(pid uint32) (claudeProcessImage, error) {
			if pid == 10 {
				return claudeProcessImage{close: func() error { closed++; return nil }}, nil
			}
			if unknown {
				return claudeProcessImage{}, errors.New("native query unavailable")
			}
			return claudeProcessImage{}, errOtherClaudeImage
		})
		if unknown {
			if err == nil || len(rows) != 0 || closed != 1 {
				t.Fatal("unknown candidate disappeared", err, rows, closed)
			}
		} else {
			if err != nil || len(rows) != 1 || closed != 0 {
				t.Fatal("known exclusion prevented selection", err, rows, closed)
			}
			if err := rows[0].close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
