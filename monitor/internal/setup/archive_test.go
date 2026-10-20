package setup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

type archiveEntry struct {
	name, data string
	mode       os.FileMode
	attrs      uint32
	store      bool
}

func archiveFixture(t *testing.T, entries ...archiveEntry) (string, Artifact) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "package.zip")
	output, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.store {
			header.Method = zip.Store
		}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		} else if strings.HasSuffix(entry.name, "/") {
			header.SetMode(os.ModeDir | 0o700)
		} else {
			header.SetMode(0o600)
		}
		header.ExternalAttrs |= entry.attrs
		member, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(member, entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return file, Artifact{Version: "v1", SourceCommit: strings.Repeat("a", 40),
		SourceURL: "https://github.com/example/app/commit/" + strings.Repeat("a", 40), Platform: "windows/amd64",
		URL:    "https://github.com/example/app/releases/download/v1/package.zip",
		SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), PeakBytes: 4 << 20,
		License: "MIT", Publisher: "example", Signature: "test fixture", InstallIdentity: "example-app", Action: "archive_per_user"}
}

func TestInstallArchivePublishesVerifiedTree(t *testing.T) {
	zipPath, pin := archiveFixture(t,
		archiveEntry{name: "App/"},
		archiveEntry{name: "App/bin/app.exe", data: "binary"},
		archiveEntry{name: "App/config.json", data: "{}"})
	dest := filepath.Join(t.TempDir(), "installed")
	if err := InstallArchive(context.Background(), pin, zipPath, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "App", "bin", "app.exe"))
	if err != nil || string(data) != "binary" {
		t.Fatalf("installed payload = %q, %v", data, err)
	}
	if installed, err := ArchiveInstalled(pin, dest); err != nil || !installed {
		t.Fatalf("verified installed receipt = %t, %v", installed, err)
	}
	wrongPin := pin
	wrongPin.SHA256 = strings.Repeat("0", 64)
	if installed, err := ArchiveInstalled(wrongPin, dest); err != nil || installed {
		t.Fatalf("other catalog pin matched = %t, %v", installed, err)
	}
	if stages, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".installed.stage-*")); len(stages) != 0 {
		t.Fatalf("staging directories retained: %v", stages)
	}
}

func TestArchiveInstalledRequiresMarkerAndIntactFiles(t *testing.T) {
	zipPath, pin := archiveFixture(t, archiveEntry{name: "App/app.exe", data: "binary"})
	dest := filepath.Join(t.TempDir(), "installed")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, err := ArchiveInstalled(pin, dest); err != nil || installed {
		t.Fatalf("unmarked directory counted installed = %t, %v", installed, err)
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), pin, zipPath, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "App", "app.exe"), []byte("tamper"), 0o600); err != nil {
		t.Fatal(err)
	}
	if installed, err := ArchiveInstalled(pin, dest); err != nil || installed {
		t.Fatalf("changed file counted installed = %t, %v", installed, err)
	}
	if err := os.Remove(filepath.Join(dest, archiveMarker)); err != nil {
		t.Fatal(err)
	}
	if installed, err := ArchiveInstalled(pin, dest); err != nil || installed {
		t.Fatalf("missing marker counted installed = %t, %v", installed, err)
	}
}

func TestArchiveInstalledRejectsUnlistedExecutableAndDirectory(t *testing.T) {
	zipPath, pin := archiveFixture(t, archiveEntry{name: "App/app.exe", data: "binary"})
	for _, name := range []string{"App/evil.dll", "extra/other.exe"} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "installed")
			if err := InstallArchive(context.Background(), pin, zipPath, dest); err != nil {
				t.Fatal(err)
			}
			extra := filepath.Join(dest, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(extra), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(extra, []byte("unapproved code"), 0o600); err != nil {
				t.Fatal(err)
			}
			if installed, err := ArchiveInstalled(pin, dest); err != nil || installed {
				t.Fatalf("unlisted executable counted installed = %t, %v", installed, err)
			}
		})
	}
}

func TestInstallArchiveRejectsUnsafeMembers(t *testing.T) {
	cases := map[string][]archiveEntry{
		"parent":         {{name: "../escape", data: "x"}},
		"absolute":       {{name: "/absolute", data: "x"}},
		"backslash":      {{name: `sub\escape`, data: "x"}},
		"drive":          {{name: `C:/escape`, data: "x"}},
		"reserved":       {{name: "App/CON.txt", data: "x"}},
		"trailing-dot":   {{name: "App/file.", data: "x"}},
		"symlink":        {{name: "link", data: "target", mode: os.ModeSymlink | 0o777}},
		"reparse":        {{name: "reparse", data: "x", attrs: 0x400 << 16}},
		"duplicate-case": {{name: "App/File", data: "a"}, {name: "app/file", data: "b"}},
		// A name the volume folds with another (Ä and ä on NTFS); names it
		// keeps apart, such as K and KELVIN SIGN, are two members.
		"unicode-case":        {{name: "App/Ä", data: "a"}, {name: "app/ä", data: "b"}},
		"file-parent":         {{name: "App", data: "a"}, {name: "App/file", data: "b"}},
		"directory-file":      {{name: "App/"}, {name: "app", data: "b"}},
		"duplicate-directory": {{name: "App/"}, {name: "app/"}},
		"reserved-marker":     {{name: ".OA-INSTALL.JSON", data: "spoof"}},
	}
	// Names that differ only in case are one name where the file system
	// compares names ignoring case (identity.SamePathName: Windows) and two
	// names elsewhere (identity CONTRACT: byte for byte). Where they are two,
	// a volume that still folds them (macOS APFS by default) refuses the
	// second member, and a case-sensitive volume installs each member as its
	// own file; neither overwrites another member or the receipt.
	folded := map[string][2]string{
		"duplicate-case":  {"App/File", "app/file"},
		"unicode-case":    {"App/Ä", "app/ä"},
		"directory-file":  {"App", "app"},
		"reserved-marker": {".OA-INSTALL.JSON", archiveMarker},
	}
	for label, entries := range cases {
		t.Run(label, func(t *testing.T) {
			zipPath, pin := archiveFixture(t, entries...)
			parent := t.TempDir()
			dest := filepath.Join(parent, "installed")
			err := InstallArchive(context.Background(), pin, zipPath, dest)
			if pair, ok := folded[label]; ok && err == nil && !identity.SamePathName(pair[0], pair[1]) {
				if installed, err := ArchiveInstalled(pin, dest); err != nil || !installed {
					t.Fatalf("members named apart did not install as the receipt lists them: %t, %v", installed, err)
				}
				for _, entry := range entries {
					if entry.data == "" {
						continue
					}
					if data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(entry.name))); err != nil || string(data) != entry.data {
						t.Fatalf("member %q holds %q, %v; want its own bytes %q", entry.name, data, err, entry.data)
					}
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe archive installed")
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe archive produced destination: %v", err)
			}
			if stages, _ := filepath.Glob(filepath.Join(parent, ".installed.stage-*")); len(stages) != 0 {
				t.Fatalf("unsafe archive retained stages: %v", stages)
			}
		})
	}
}

func TestInstallArchiveRejectsWrongPinOverflowAndExistingApp(t *testing.T) {
	zipPath, pin := archiveFixture(t, archiveEntry{name: "app.exe", data: strings.Repeat("x", 1<<20)})
	parent := t.TempDir()
	dest := filepath.Join(parent, "installed")
	wrongSize := pin
	wrongSize.Bytes++
	if err := InstallArchive(context.Background(), wrongSize, zipPath, dest); err == nil {
		t.Fatal("wrong compressed size accepted")
	}
	wrongHash := pin
	wrongHash.SHA256 = strings.Repeat("0", 64)
	if err := InstallArchive(context.Background(), wrongHash, zipPath, dest); err == nil {
		t.Fatal("wrong digest accepted")
	}
	overflow := pin
	overflow.PeakBytes = pin.Bytes + 1
	if err := InstallArchive(context.Background(), overflow, zipPath, dest); err == nil {
		t.Fatal("expanded payload beyond catalog bound accepted")
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dest, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), pin, zipPath, dest); err == nil {
		t.Fatal("existing app was overwritten")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("existing app changed: %q, %v", data, err)
	}
}

func TestInstallArchiveCleansFailedExtractionAndCancellation(t *testing.T) {
	zipPath, pin := archiveFixture(t, archiveEntry{name: "app.exe", data: "verify-this-body", store: true})
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	index := bytes.Index(data, []byte("verify-this-body"))
	if index < 0 {
		t.Fatal("fixture body absent")
	}
	data[index] ^= 0xff
	if err := os.WriteFile(zipPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	pin.SHA256 = hex.EncodeToString(sum[:])
	parent := t.TempDir()
	dest := filepath.Join(parent, "installed")
	if err := InstallArchive(context.Background(), pin, zipPath, dest); err == nil {
		t.Fatal("corrupt member stream installed")
	}
	if stages, _ := filepath.Glob(filepath.Join(parent, ".installed.stage-*")); len(stages) != 0 {
		t.Fatalf("failed extraction retained stages: %v", stages)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := InstallArchive(ctx, pin, zipPath, dest); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled install = %v", err)
	}
	var expanded int64
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	writer := cancelWriter{cancel: cancel}
	if err := copyBounded(ctx, &writer, bytes.NewReader(bytes.Repeat([]byte("a"), archiveChunk*2)), make([]byte, archiveChunk), &expanded, archiveChunk*3); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-copy cancellation = %v", err)
	}
}

type cancelWriter struct{ cancel context.CancelFunc }

func (w *cancelWriter) Write(data []byte) (int, error) {
	w.cancel()
	return len(data), nil
}
