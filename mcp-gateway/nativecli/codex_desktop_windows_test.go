package nativecli

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func codexDesktopPE(t *testing.T, path string, subsystem uint16) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	dos := make([]byte, 128)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], 128)
	var out bytes.Buffer
	out.Write(dos)
	out.Write([]byte{'P', 'E', 0, 0})
	h := pe.OptionalHeader64{Magic: 0x20b, Subsystem: subsystem, NumberOfRvaAndSizes: 16}
	if err := binary.Write(&out, binary.LittleEndian, pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, SizeOfOptionalHeader: uint16(binary.Size(h)), Characteristics: 2}); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&out, binary.LittleEndian, h); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func codexDesktopFixture(t *testing.T) (string, []claudeProcessImage, *bool, *int) {
	t.Helper()
	root := t.TempDir()
	gui := filepath.Join(root, "app", "ChatGPT.exe")
	codexDesktopPE(t, gui, pe.IMAGE_SUBSYSTEM_WINDOWS_GUI)
	codexDesktopPE(t, filepath.Join(root, "app", "resources", "codex.exe"), pe.IMAGE_SUBSYSTEM_WINDOWS_CUI)
	alive, closed := true, 0
	var rows []claudeProcessImage
	for i := 0; i < 2; i++ {
		image := claudeCLIImageFixture(t, gui)
		rows = append(rows, claudeProcessImage{image: image, current: func() error {
			if !alive {
				return errProductProcessEnded
			}
			return nil
		}, close: func() error { closed++; return image.Close() }})
	}
	return root, rows, &alive, &closed
}

func codexDesktopSelection(t *testing.T, root string, rows []claudeProcessImage, current *string) (*CodexCLI, error) {
	t.Helper()
	return selectCodexDesktop(context.Background(), rows, codexDesktopOpener(t, root, current))
}

type codexFixtureImage struct {
	*Image
	current *string
}

func (i *codexFixtureImage) Check(ctx context.Context) error {
	if *i.current != "OpenAI.Codex_1.0.0.0_x64__2p2nqsd0c76g0" {
		return errors.New("native registration changed")
	}
	return i.Image.Check(ctx)
}
func codexDesktopOpener(t *testing.T, root string, current *string) func(context.Context, string) (nativeProductImage, error) {
	return func(_ context.Context, relative string) (nativeProductImage, error) {
		if !filepath.IsAbs(root) {
			return nil, errors.New("registered owner refused relative root")
		}
		return &codexFixtureImage{Image: claudeCLIImageFixture(t, filepath.Join(root, filepath.FromSlash(relative))), current: current}, nil
	}
}

func TestCodexDesktopGUIPathDoesNotNeedInternalCLIOnPATH(t *testing.T) {
	root, rows, alive, closed := codexDesktopFixture(t)
	current := "OpenAI.Codex_1.0.0.0_x64__2p2nqsd0c76g0"
	probes := 0
	selected, err := selectCodexCLI(context.Background(), "", func(ctx context.Context, path string) ([]byte, error) {
		probes++
		if path != filepath.Join(root, "app", "resources", "codex.exe") {
			t.Fatal(path)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err == nil {
			t.Fatal("bundled CLI allowed replacement")
		}
		return []byte("codex-cli 0.164.0-alpha.11.1\n"), nil
	}, func(context.Context) (*CodexCLI, error) { return codexDesktopSelection(t, root, rows, &current) })
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if probes != 1 || selected.Version() != "0.164.0-alpha.11.1" || selected.FileKey() == "" {
		t.Fatal(probes, selected.Version())
	}
	current = "OpenAI.Codex_2.0.0.0_x64__2p2nqsd0c76g0"
	if err := selected.Check(context.Background()); err == nil {
		t.Fatal("changed registered package remained usable")
	}
	current = "OpenAI.Codex_1.0.0.0_x64__2p2nqsd0c76g0"
	*alive = false
	if err := selected.Check(context.Background()); err != nil {
		t.Fatal("registered installation unavailable after desktop quit", err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	if *closed != 2 {
		t.Fatal("process holds leaked", *closed)
	}
}

func TestCodexDesktopStoppedHostUsesCurrentRegisteredInstallation(t *testing.T) {
	root, rows, _, _ := codexDesktopFixture(t)
	for _, row := range rows {
		if err := row.close(); err != nil {
			t.Fatal(err)
		}
	}
	current := "OpenAI.Codex_1.0.0.0_x64__2p2nqsd0c76g0"
	selected, err := codexDesktopSelection(t, root, nil, &current)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if selected == nil || selected.Check(context.Background()) != nil {
		t.Fatal("stopped registered desktop unavailable")
	}
	current = "OpenAI.Codex_2.0.0.0_x64__2p2nqsd0c76g0"
	if err := selected.Check(context.Background()); err == nil {
		t.Fatal("changed registration remained usable after stopped-host selection")
	}
}

func TestCodexDesktopRefusesRelativeAmbiguousForeignAndUnknownEvidence(t *testing.T) {
	for _, mode := range []string{"relative", "ambiguous", "foreign", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			root, rows, _, closed := codexDesktopFixture(t)
			if mode == "relative" {
				root = "relative"
			}
			if mode == "foreign" {
				path := filepath.Join(t.TempDir(), "ChatGPT.exe")
				codexDesktopPE(t, path, pe.IMAGE_SUBSYSTEM_WINDOWS_GUI)
				rows[1].image.Close()
				image := claudeCLIImageFixture(t, path)
				rows[1].image = image
				rows[1].close = func() error { *closed++; return image.Close() }
			}
			if mode == "unknown" {
				rows[1].current = func() error { return errors.New("native query unavailable") }
			}
			current := "OpenAI.Codex_1.0.0.0_x64__2p2nqsd0c76g0"
			open := codexDesktopOpener(t, root, &current)
			if mode == "ambiguous" {
				open = func(context.Context, string) (nativeProductImage, error) {
					return nil, errors.New("native registration ambiguous")
				}
			}
			if selected, err := selectCodexDesktop(context.Background(), rows, open); err == nil {
				selected.Close()
				t.Fatal("unsupported evidence accepted")
			}
			if *closed != 2 {
				t.Fatal("refusal leaked process holds", *closed)
			}
		})
	}
}

func TestCodexDesktopUnknownRefusesBeforePATHFallbackAndKnownEndCanRetire(t *testing.T) {
	path, _ := codexCLIFixture(t)
	probed := false
	if _, err := selectCodexCLI(context.Background(), path, func(context.Context, string) ([]byte, error) { probed = true; return []byte("codex-cli 1.2.3"), nil }, func(context.Context) (*CodexCLI, error) { return nil, errors.New("native inventory unknown") }); err == nil || probed {
		t.Fatal("unknown desktop fell back to PATH", err, probed)
	}
	root, rows, _, closed := codexDesktopFixture(t)
	rows[0].current = func() error { return errProductProcessEnded }
	current := "OpenAI.Codex_1.0.0.0_x64__2p2nqsd0c76g0"
	selected, err := codexDesktopSelection(t, root, rows, &current)
	if err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	if *closed != 2 {
		t.Fatal(*closed)
	}
}

func TestCodexDesktopReadOnlyCurrentPackage(t *testing.T) {
	if os.Getenv("OA_CODEX_DESKTOP_READONLY") != "1" {
		t.Skip("explicit current-user package metadata check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	selected, err := currentCodexDesktop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil {
		t.Fatal("current registered desktop was not selected")
	}
	defer selected.Close()
	if selected.desktopCheck == nil || selected.FileKey() == "" {
		t.Fatal("missing native desktop evidence")
	}
	if err := selected.Check(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("current registered package/native GUI/bundled CLI holds PASS; no vendor command executed")
}
